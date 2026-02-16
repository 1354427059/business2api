import logging
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Any, Dict, List, Optional

from gemini_refresh import refresh_one
from gemini_register import register_one
from go_client import Business2APIClient
from settings import Settings

logger = logging.getLogger(__name__)


class RegistrarScheduler:
	def __init__(self, settings: Settings):
		self.settings = settings
		self.client = Business2APIClient(settings)
		self._stop_event = threading.Event()
		self._register_lock = threading.Lock()
		self._refresh_lock = threading.Lock()
		self._metrics_lock = threading.Lock()
		self._threads: List[threading.Thread] = []
		self._metrics: Dict[str, Any] = {
			"register_success": 0,
			"register_failed": 0,
			"refresh_success": 0,
			"refresh_failed": 0,
			"last_error": "",
			"last_register_run": 0,
			"last_refresh_run": 0,
		}

	def start(self) -> None:
		if self.settings.enable_auto_register:
			t = threading.Thread(target=self._register_loop, daemon=True, name="register-loop")
			t.start()
			self._threads.append(t)
		if self.settings.enable_auto_refresh:
			t = threading.Thread(target=self._refresh_loop, daemon=True, name="refresh-loop")
			t.start()
			self._threads.append(t)
		logger.info("scheduler 启动完成: register=%s refresh=%s", self.settings.enable_auto_register, self.settings.enable_auto_refresh)

	def stop(self) -> None:
		self._stop_event.set()
		for t in self._threads:
			t.join(timeout=3)

	def snapshot(self) -> Dict[str, Any]:
		with self._metrics_lock:
			data = dict(self._metrics)
		data["running"] = not self._stop_event.is_set()
		return data

	def trigger_register(self, count: int = 1) -> Dict[str, Any]:
		threading.Thread(target=self._run_register_batch, args=(count,), daemon=True).start()
		return {"accepted": True, "count": count}

	def trigger_refresh(self, limit: Optional[int] = None) -> Dict[str, Any]:
		threading.Thread(target=self._run_refresh_cycle, args=(limit,), daemon=True).start()
		return {"accepted": True, "limit": limit}

	def _set_error(self, err: Exception) -> None:
		logger.error("%s", err)
		with self._metrics_lock:
			self._metrics["last_error"] = str(err)

	def _register_loop(self) -> None:
		while not self._stop_event.is_set():
			try:
				status = self.client.get_status()
				target = int(status.get("target", 0))
				total = int(status.get("total", 0))
				need = max(target - total, 0)
				if need > 0:
					batch = min(need, max(self.settings.register_max_workers, 1))
					self._run_register_batch(batch)
			except Exception as exc:
				self._set_error(exc)
			self._stop_event.wait(self.settings.register_interval_sec)

	def _refresh_loop(self) -> None:
		while not self._stop_event.is_set():
			try:
				self._run_refresh_cycle(self.settings.refresh_batch_limit)
			except Exception as exc:
				self._set_error(exc)
			self._stop_event.wait(self.settings.refresh_interval_sec)

	def _run_register_batch(self, count: int) -> None:
		count = max(0, int(count))
		if count == 0:
			return
		if not self._register_lock.acquire(blocking=False):
			logger.info("register 批次正在执行，跳过本次请求")
			return
		try:
			workers = min(max(1, self.settings.register_max_workers), count)
			with ThreadPoolExecutor(max_workers=workers) as executor:
				futures = [executor.submit(register_one, self.settings) for _ in range(count)]
				for future in as_completed(futures):
					try:
						payload = future.result()
						self.client.upload_account(payload)
						with self._metrics_lock:
							self._metrics["register_success"] += 1
					except Exception as exc:
						self._set_error(exc)
						with self._metrics_lock:
							self._metrics["register_failed"] += 1
			with self._metrics_lock:
				self._metrics["last_register_run"] = int(time.time())
		finally:
			self._register_lock.release()

	def _run_refresh_cycle(self, limit: Optional[int]) -> None:
		if not self._refresh_lock.acquire(blocking=False):
			logger.info("refresh 批次正在执行，跳过本次请求")
			return
		try:
			task_limit = self.settings.refresh_batch_limit if limit is None else max(1, int(limit))
			tasks = self.client.get_refresh_tasks(limit=task_limit)
			if not tasks:
				return
			for task in tasks:
				try:
					payload = refresh_one(self.settings, task)
					self.client.upload_account(payload)
					with self._metrics_lock:
						self._metrics["refresh_success"] += 1
				except Exception as exc:
					self._set_error(exc)
					with self._metrics_lock:
						self._metrics["refresh_failed"] += 1
			with self._metrics_lock:
				self._metrics["last_refresh_run"] = int(time.time())
		finally:
			self._refresh_lock.release()
