import base64
import json
import logging
import re
from typing import Any, Dict, List
from urllib.parse import parse_qs, urlparse

logger = logging.getLogger(__name__)

_CONFIG_ID_RE = re.compile(r"/cid/([a-f0-9-]+)")
_URL_AUTH_RE = re.compile(r"[?&](?:token|auth)=([^&]+)", re.IGNORECASE)
_PAGE_AUTH_RE = re.compile(r'"authorization"\s*:\s*"([^"]+)"', re.IGNORECASE)


def build_cookie_string(cookies: List[Dict[str, str]]) -> str:
	parts: List[str] = []
	for item in cookies:
		name = str(item.get("name", "")).strip()
		if not name:
			continue
		value = str(item.get("value", ""))
		parts.append(f"{name}={value}")
	return "; ".join(parts)


def extract_config_id(url: str) -> str:
	if not url:
		return ""
	match = _CONFIG_ID_RE.search(url)
	return match.group(1) if match else ""


def extract_csesidx_from_url(url: str) -> str:
	if not url:
		return ""
	query = parse_qs(urlparse(url).query)
	return (query.get("csesidx") or [""])[0]


def extract_csesidx_from_authorization(auth_header: str) -> str:
	if not auth_header or " " not in auth_header:
		return ""
	token = auth_header.split(" ", 1)[1]
	parts = token.split(".")
	if len(parts) != 3:
		return ""
	payload = parts[1]
	padding = "=" * ((4 - len(payload) % 4) % 4)
	try:
		decoded = base64.urlsafe_b64decode(payload + padding)
		claims = json.loads(decoded.decode("utf-8"))
	except Exception:
		return ""
	sub = str(claims.get("sub", ""))
	if sub.startswith("csesidx/"):
		return sub.split("/", 1)[1]
	return ""


def collect_authorization_and_ids(driver: Any) -> Dict[str, str]:
	auth_header = ""
	authorization_source = ""
	config_id = ""
	csesidx = ""

	try:
		logs = driver.get_log("performance")
	except Exception:
		logs = []

	for entry in reversed(logs):
		try:
			message = json.loads(entry.get("message", "{}")).get("message", {})
			method = str(message.get("method", ""))
			params = message.get("params", {}) or {}
			headers = {}
			url = ""
			if method == "Network.requestWillBeSent":
				request = params.get("request", {}) or {}
				headers = request.get("headers", {}) or {}
				url = str(request.get("url", ""))
			elif method in ("Network.requestWillBeSentExtraInfo", "Network.responseReceivedExtraInfo"):
				headers = params.get("headers", {}) or {}
			else:
				continue
			if not auth_header:
				candidate_auth = str(headers.get("authorization") or headers.get("Authorization") or "").strip()
				if candidate_auth:
					auth_header = candidate_auth
					authorization_source = "network"
			if url:
				if not config_id:
					config_id = extract_config_id(url)
				if not csesidx:
					csesidx = extract_csesidx_from_url(url)
			if auth_header and config_id and csesidx:
				break
		except Exception:
			continue

	current_url = ""
	try:
		current_url = str(driver.current_url)
	except Exception:
		pass

	if not config_id:
		config_id = extract_config_id(current_url)
	if not csesidx:
		csesidx = extract_csesidx_from_url(current_url)
	if not auth_header:
		try:
			storage_auth = driver.execute_script(
				"""
const keys = ['Authorization', 'authorization', 'auth_token', 'token'];
for (const key of keys) {
  const localVal = window.localStorage ? localStorage.getItem(key) : null;
  if (localVal) return localVal;
  const sessionVal = window.sessionStorage ? sessionStorage.getItem(key) : null;
  if (sessionVal) return sessionVal;
}
return '';
"""
			)
			if isinstance(storage_auth, str) and storage_auth.strip():
				auth_header = storage_auth.strip()
				authorization_source = "storage"
		except Exception:
			pass
	if not auth_header:
		try:
			source = str(driver.page_source or "")
			match = _PAGE_AUTH_RE.search(source)
			if match:
				auth_header = match.group(1).strip()
				authorization_source = "page"
		except Exception:
			pass
	if not auth_header and current_url:
		match = _URL_AUTH_RE.search(current_url)
		if match:
			auth_header = match.group(1).strip()
			authorization_source = "url"
	if not csesidx and auth_header:
		csesidx = extract_csesidx_from_authorization(auth_header)

	return {
		"authorization": auth_header.strip(),
		"authorization_source": authorization_source.strip(),
		"config_id": config_id.strip(),
		"csesidx": csesidx.strip(),
		"current_url": current_url.strip(),
	}


def collect_cookies(driver: Any) -> List[Dict[str, str]]:
	try:
		raw = driver.get_cookies() or []
	except Exception:
		raw = []

	allowed_domains = (
		".gemini.google",
		"business.gemini.google",
		".google.com",
		"accounts.google.com",
	)

	cookies: List[Dict[str, str]] = []
	for item in raw:
		name = str(item.get("name", "")).strip()
		value = str(item.get("value", ""))
		domain = str(item.get("domain", "")).strip()
		if not name or value == "":
			continue
		if domain and not any(domain.endswith(suffix) for suffix in allowed_domains):
			continue
		if not domain:
			domain = ".gemini.google"
		cookies.append({"name": name, "value": value, "domain": domain})
	return cookies


def build_upload_payload(
	driver: Any,
	email: str,
	full_name: str,
	mail_provider: str,
	mail_password: str,
	is_new: bool,
) -> Dict[str, Any]:
	snapshot = collect_authorization_and_ids(driver)
	cookies = collect_cookies(driver)
	cookie_string = build_cookie_string(cookies)
	authorization = snapshot.get("authorization", "").strip()
	authorization_source = snapshot.get("authorization_source", "").strip()
	config_id = snapshot.get("config_id", "").strip()
	csesidx = snapshot.get("csesidx", "").strip()
	fallback_used = False
	if not authorization and config_id and csesidx:
		authorization = f"Bearer fallback-csesidx-{csesidx}"
		authorization_source = "fallback"
		fallback_used = True
		logger.warning("authorization 缺失，使用 fallback 令牌: email=%s", email)
	elif authorization and not authorization_source:
		authorization_source = "network"

	payload = {
		"email": email,
		"full_name": full_name,
		"mail_provider": mail_provider,
		"mail_password": mail_password,
		"cookies": cookies,
		"cookie_string": cookie_string,
		"authorization": authorization,
		"authorization_source": authorization_source,
		"fallback_used": fallback_used,
		"config_id": config_id,
		"csesidx": csesidx,
		"is_new": is_new,
	}
	validate_payload(payload)
	return payload


def validate_payload(payload: Dict[str, Any]) -> None:
	required = ["email", "cookies", "authorization", "config_id", "csesidx"]
	for key in required:
		value = payload.get(key)
		if key == "cookies":
			if not isinstance(value, list) or len(value) == 0:
				raise ValueError("payload 缺少 cookies")
			continue
		if not isinstance(value, str) or not value.strip():
			raise ValueError(f"payload 缺少 {key}")
	source = str(payload.get("authorization_source", "")).strip().lower()
	if not source:
		raise ValueError("payload 缺少 authorization_source")
	if source not in {"network", "storage", "page", "url", "fallback"}:
		raise ValueError(f"payload authorization_source 无效: {source}")
	if payload.get("fallback_used") and source != "fallback":
		raise ValueError("payload fallback_used 与 authorization_source 不一致")
	if payload.get("is_new") and not str(payload.get("full_name", "")).strip():
		raise ValueError("payload 缺少 full_name（注册场景）")
