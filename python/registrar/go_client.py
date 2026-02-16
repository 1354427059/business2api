import logging
from typing import Any, Dict, List

import requests
from tenacity import retry, stop_after_attempt, wait_fixed

from settings import Settings

logger = logging.getLogger(__name__)


class Business2APIClient:
	def __init__(self, settings: Settings):
		self.base_url = settings.b2a_base_url.rstrip("/")
		self.timeout = settings.request_timeout_sec
		self.session = requests.Session()
		headers = {"Content-Type": "application/json"}
		if settings.b2a_api_key:
			headers["Authorization"] = f"Bearer {settings.b2a_api_key}"
		self.session.headers.update(headers)

	@retry(stop=stop_after_attempt(3), wait=wait_fixed(2), reraise=True)
	def _request(self, method: str, path: str, **kwargs) -> requests.Response:
		url = f"{self.base_url}{path}"
		resp = self.session.request(method, url, timeout=self.timeout, **kwargs)
		if resp.status_code >= 500:
			resp.raise_for_status()
		return resp

	def get_status(self) -> Dict[str, Any]:
		resp = self._request("GET", "/admin/status")
		resp.raise_for_status()
		return resp.json()

	def upload_account(self, payload: Dict[str, Any]) -> Dict[str, Any]:
		resp = self._request("POST", "/admin/registrar/upload-account", json=payload)
		if resp.status_code >= 400:
			try:
				data = resp.json()
			except Exception:
				data = {"error": resp.text}
			raise RuntimeError(f"上传账号失败: status={resp.status_code}, detail={data}")
		data = resp.json()
		if not data.get("success", False):
			raise RuntimeError(f"上传账号失败: {data}")
		return data

	def get_refresh_tasks(self, limit: int = 20) -> List[Dict[str, Any]]:
		resp = self._request("GET", f"/admin/registrar/refresh-tasks?limit={limit}")
		resp.raise_for_status()
		data = resp.json()
		tasks = data.get("tasks", [])
		if not isinstance(tasks, list):
			logger.warning("refresh tasks 格式异常: %s", data)
			return []
		return tasks

	def claim_refresh_tasks(self, worker_id: str, limit: int = 20, lease_sec: int = 180) -> Dict[str, Any]:
		payload = {
			"worker_id": worker_id,
			"limit": max(1, int(limit)),
			"lease_sec": max(1, int(lease_sec)),
		}
		resp = self._request("POST", "/admin/registrar/refresh-tasks/claim", json=payload)
		if resp.status_code >= 400:
			try:
				data = resp.json()
			except Exception:
				data = {"error": resp.text}
			raise RuntimeError(f"claim 续期任务失败: status={resp.status_code}, detail={data}")
		data = resp.json()
		tasks = data.get("tasks", [])
		if not isinstance(tasks, list):
			tasks = []
		return {
			"tasks": tasks,
			"count": int(data.get("count", len(tasks))),
			"throttled": bool(data.get("throttled", False)),
		}

	def fail_refresh_task(self, task_id: str, worker_id: str, err_msg: str) -> Dict[str, Any]:
		payload = {
			"task_id": str(task_id or "").strip(),
			"worker_id": str(worker_id or "").strip(),
			"error": str(err_msg or "").strip(),
		}
		resp = self._request("POST", "/admin/registrar/refresh-tasks/fail", json=payload)
		if resp.status_code >= 400:
			try:
				data = resp.json()
			except Exception:
				data = {"error": resp.text}
			raise RuntimeError(f"回报续期失败任务失败: status={resp.status_code}, detail={data}")
		return resp.json()
