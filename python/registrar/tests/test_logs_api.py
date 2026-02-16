import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))

from log_buffer import InMemoryLogBuffer

try:
	import app as registrar_app
except ModuleNotFoundError:
	registrar_app = None


class LogBufferTest(unittest.TestCase):
	def test_ring_buffer_order_and_trim(self):
		buf = InMemoryLogBuffer(capacity=100)
		for i in range(1, 105):
			buf.append("info", f"line-{i}")

		snap = buf.snapshot(after_id=0, limit=10, level="all")
		items = snap["items"]
		self.assertEqual(len(items), 10)
		self.assertEqual(items[0]["message"], "line-95")
		self.assertEqual(items[-1]["message"], "line-104")
		self.assertEqual(snap["next_after_id"], items[-1]["id"])

	@unittest.skipIf(registrar_app is None, "fastapi 未安装，跳过 /logs 接口测试")
	def test_logs_endpoint_filters(self):
		buf = InMemoryLogBuffer(capacity=10)
		buf.append("info", "info-message")
		buf.append("warn", "warn-message")
		buf.append("error", "error-message")

		old_buffer = registrar_app.log_buffer
		registrar_app.log_buffer = buf
		try:
			error_only = registrar_app.logs(after_id=0, limit=10, level="error")
			self.assertEqual(len(error_only["items"]), 1)
			self.assertEqual(error_only["items"][0]["message"], "error-message")

			after_second = registrar_app.logs(after_id=2, limit=10, level="all")
			self.assertEqual(len(after_second["items"]), 1)
			self.assertEqual(after_second["items"][0]["message"], "error-message")
			self.assertEqual(after_second["next_after_id"], after_second["items"][0]["id"])
		finally:
			registrar_app.log_buffer = old_buffer


if __name__ == "__main__":
	unittest.main()
