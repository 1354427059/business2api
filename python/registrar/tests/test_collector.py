import base64
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))

from collector import (
	build_cookie_string,
	extract_config_id,
	extract_csesidx_from_authorization,
	extract_csesidx_from_url,
	validate_payload,
)


class CollectorTest(unittest.TestCase):
	def test_extract_config_id(self):
		url = "https://business.gemini.google/cid/abc-def-123?csesidx=88"
		self.assertEqual(extract_config_id(url), "abc-def-123")

	def test_extract_csesidx_from_url(self):
		url = "https://business.gemini.google/a?x=1&csesidx=9988"
		self.assertEqual(extract_csesidx_from_url(url), "9988")

	def test_extract_csesidx_from_authorization(self):
		header = base64.urlsafe_b64encode(json.dumps({"alg": "HS256"}).encode()).decode().rstrip("=")
		payload = base64.urlsafe_b64encode(json.dumps({"sub": "csesidx/778899"}).encode()).decode().rstrip("=")
		token = f"{header}.{payload}.sig"
		auth = f"Bearer {token}"
		self.assertEqual(extract_csesidx_from_authorization(auth), "778899")

	def test_build_cookie_string(self):
		cookies = [{"name": "a", "value": "1"}, {"name": "b", "value": "2"}]
		self.assertEqual(build_cookie_string(cookies), "a=1; b=2")

	def test_validate_payload_missing_required(self):
		payload = {
			"email": "demo@example.com",
			"cookies": [{"name": "a", "value": "1", "domain": ".gemini.google"}],
			"authorization": "Bearer token",
			"config_id": "cfg",
			"csesidx": "123",
			"is_new": False,
		}
		validate_payload(payload)

		missing_auth = dict(payload)
		missing_auth["authorization"] = ""
		with self.assertRaises(ValueError):
			validate_payload(missing_auth)

		missing_cookies = dict(payload)
		missing_cookies["cookies"] = []
		with self.assertRaises(ValueError):
			validate_payload(missing_cookies)

		missing_name_for_new = dict(payload)
		missing_name_for_new["is_new"] = True
		missing_name_for_new["full_name"] = ""
		with self.assertRaises(ValueError):
			validate_payload(missing_name_for_new)


if __name__ == "__main__":
	unittest.main()
