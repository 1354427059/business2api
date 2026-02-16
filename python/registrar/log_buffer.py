import logging
import threading
from datetime import datetime, timezone
from typing import Dict, List


def normalize_level(level: str) -> str:
	value = (level or "").strip().lower()
	mapping = {
		"critical": "error",
		"fatal": "error",
		"error": "error",
		"warning": "warn",
		"warn": "warn",
		"info": "info",
		"debug": "debug",
		"all": "all",
	}
	return mapping.get(value, "info")


class InMemoryLogBuffer(logging.Handler):
	def __init__(self, capacity: int = 5000):
		super().__init__()
		self.capacity = max(100, capacity)
		self._items: List[Dict[str, object]] = []
		self._next_id = 0
		self._lock = threading.Lock()

	def emit(self, record: logging.LogRecord) -> None:
		try:
			message = self.format(record)
		except Exception:
			message = record.getMessage()
		self.append(normalize_level(record.levelname), message)

	def append(self, level: str, message: str) -> Dict[str, object]:
		clean = (message or "").strip()
		if not clean:
			return {}
		entry: Dict[str, object] = {
			"id": 0,
			"ts": datetime.now(timezone.utc).isoformat(),
			"level": normalize_level(level),
			"message": clean,
			"source": "registrar",
		}
		with self._lock:
			self._next_id += 1
			entry["id"] = self._next_id
			self._items.append(entry)
			if len(self._items) > self.capacity:
				self._items = self._items[-self.capacity :]
		return entry

	def snapshot(self, after_id: int = 0, limit: int = 200, level: str = "all") -> Dict[str, object]:
		level_filter = normalize_level(level)
		limit = max(1, min(1000, int(limit)))
		after_id = max(0, int(after_id))

		with self._lock:
			items = list(self._items)

		if level_filter != "all":
			items = [item for item in items if normalize_level(str(item.get("level", ""))) == level_filter]

		if after_id <= 0:
			result = items[-limit:]
			has_more = len(items) > len(result)
		else:
			candidates = [item for item in items if int(item.get("id", 0)) > after_id]
			result = candidates[:limit]
			has_more = len(candidates) > len(result)

		next_after_id = after_id
		if result:
			next_after_id = int(result[-1].get("id", after_id))

		return {
			"items": result,
			"next_after_id": next_after_id,
			"has_more": has_more,
		}
