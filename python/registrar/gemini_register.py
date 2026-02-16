import html
import logging
import os
import random
import re
import time
from datetime import datetime, timezone
from typing import Any, Dict, Optional

import requests
from selenium import webdriver
from selenium.common.exceptions import TimeoutException
from selenium.webdriver.common.by import By
from selenium.webdriver.support import expected_conditions as EC
from selenium.webdriver.support.ui import WebDriverWait

from collector import build_upload_payload, collect_authorization_and_ids
from settings import Settings

logger = logging.getLogger(__name__)

SCRIPT_EMAIL_XPATH = "/html/body/c-wiz/div/div/div[1]/div/div/div/form/div[1]/div[1]/div/span[2]/input"
SCRIPT_CONTINUE_XPATH = "/html/body/c-wiz/div/div/div[1]/div/div/div/form/div[2]/div/button"
SCRIPT_VERIFY_XPATH = "/html/body/c-wiz/div/div/div[1]/div/div/div/form/div[2]/div/div[1]/span/div[1]/button"

NAMES = [
	"James Smith",
	"John Johnson",
	"Robert Williams",
	"Michael Brown",
	"William Jones",
	"David Garcia",
	"Mary Miller",
	"Patricia Davis",
]


def _save_debug_artifact(driver: Any, settings: Settings, prefix: str) -> None:
	os.makedirs(settings.artifacts_dir, exist_ok=True)
	ts = int(time.time())
	png_path = os.path.join(settings.artifacts_dir, f"{prefix}_{ts}.png")
	html_path = os.path.join(settings.artifacts_dir, f"{prefix}_{ts}.html")
	try:
		driver.save_screenshot(png_path)
	except Exception:
		pass
	try:
		with open(html_path, "w", encoding="utf-8") as f:
			f.write(driver.page_source)
	except Exception:
		pass


def _build_driver(settings: Settings) -> Any:
	options = webdriver.ChromeOptions()
	if settings.headless:
		options.add_argument("--headless=new")
	options.add_argument("--no-sandbox")
	options.add_argument("--disable-dev-shm-usage")
	options.add_argument("--disable-blink-features=AutomationControlled")
	options.add_experimental_option("excludeSwitches", ["enable-automation"])
	options.add_experimental_option("useAutomationExtension", False)
	options.set_capability("goog:loggingPrefs", {"performance": "ALL", "browser": "ALL"})
	driver = webdriver.Remote(command_executor=settings.selenium_remote_url, options=options)
	try:
		driver.execute_cdp_cmd(
			"Page.addScriptToEvaluateOnNewDocument",
			{
				"source": """
Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
Object.defineProperty(navigator, 'languages', {get: () => ['en-US', 'en']});
Object.defineProperty(navigator, 'plugins', {get: () => [1, 2, 3, 4, 5]});
""",
			},
		)
	except Exception as exc:
		logger.warning("注入反自动化脚本失败: %s", exc)
	driver.set_window_size(1600, 960)
	driver.set_page_load_timeout(90)
	return driver


def _mail_headers(settings: Settings) -> Dict[str, str]:
	if not settings.mail_key:
		raise RuntimeError("MAIL_KEY 未配置，无法访问邮箱服务")
	return {
		"X-API-Key": settings.mail_key,
		"User-Agent": "Mozilla/5.0 (compatible; registrar/1.0)",
	}


def create_temp_email(settings: Settings) -> str:
	url = f"{settings.mail_api}/api/generate-email"
	resp = requests.get(url, headers=_mail_headers(settings), timeout=settings.request_timeout_sec)
	resp.raise_for_status()
	data = resp.json()
	if not data.get("success"):
		raise RuntimeError(f"邮箱创建失败: {data}")
	email = (data.get("data") or {}).get("email", "")
	if not email or "@" not in email:
		raise RuntimeError(f"邮箱响应异常: {data}")
	return email


def get_email_count(settings: Settings, email: str) -> int:
	url = f"{settings.mail_api}/api/emails"
	resp = requests.get(
		url,
		params={"email": email},
		headers=_mail_headers(settings),
		timeout=settings.request_timeout_sec,
	)
	if resp.status_code != 200:
		return 0
	data = resp.json()
	emails = (data.get("data") or {}).get("emails") or []
	return len(emails)


def _extract_code_from_email(raw: str) -> Optional[str]:
	text = html.unescape(raw or "")
	plain_text = re.sub(r"<[^>]+>", " ", text)
	plain_text = re.sub(r"\s+", " ", plain_text)
	match = re.search(
		r"(?:one[- ]?time\s+verification\s+code\s+is|verification\s+code\s+is|verification\s+code)\D*([A-Z0-9]{6})",
		plain_text,
		re.IGNORECASE,
	)
	if match:
		return match.group(1).upper()
	match = re.search(r"verification-code[^>]*>(\d{6})<", text, re.IGNORECASE)
	if match:
		return match.group(1)
	match = re.search(r"(?<!\d)(\d{6})(?!\d)", text)
	if match:
		return match.group(1)
	for candidate in re.findall(r"(?<![A-Z0-9])([A-Z0-9]{6})(?![A-Z0-9])", plain_text.upper()):
		if any(ch.isdigit() for ch in candidate):
			return candidate
	return None


def _mail_contents(item: Dict[str, Any]) -> list[str]:
	contents = []
	for key in ("content", "text_content", "html_content"):
		value = item.get(key)
		if isinstance(value, str) and value.strip():
			contents.append(value)
	return contents


def _mail_identity(item: Dict[str, Any]) -> str:
	content = "||".join(_mail_contents(item))
	content_sig = f"{hash(content[:400])}:{len(content)}"
	for key in ("id", "mail_id", "message_id", "uuid"):
		value = item.get(key)
		if value:
			return f"id:{value}:{content_sig}"
	subject = item.get("subject") or ""
	return f"fallback:{subject}:{content_sig}"


def _mail_preview(raw: str, limit: int = 220) -> str:
	text = re.sub(r"<[^>]+>", " ", raw or "")
	text = re.sub(r"\s+", " ", html.unescape(text)).strip()
	if len(text) <= limit:
		return text
	return text[:limit] + "..."


def _mail_timestamp(item: Dict[str, Any]) -> Optional[int]:
	ts = item.get("timestamp")
	if isinstance(ts, (int, float)):
		return int(ts)
	if isinstance(ts, str) and ts.isdigit():
		return int(ts)
	created_at = item.get("created_at")
	if isinstance(created_at, str) and created_at:
		try:
			dt = datetime.fromisoformat(created_at.replace("Z", "+00:00"))
			if dt.tzinfo is None:
				dt = dt.replace(tzinfo=timezone.utc)
			return int(dt.timestamp())
		except Exception:
			return None
	return None


def wait_verification_code(
	settings: Settings,
	email: str,
	initial_count: int = 0,
) -> str:
	url = f"{settings.mail_api}/api/emails"
	start = time.time()
	attempt = 0
	seen_mails = set()
	initialized_baseline = False
	fresh_cutoff = int(start) - 120
	while time.time() - start < settings.mail_poll_timeout_sec:
		attempt += 1
		resp = requests.get(
			url,
			params={"email": email},
			headers=_mail_headers(settings),
			timeout=settings.request_timeout_sec,
		)
		if resp.status_code == 200:
			data = resp.json()
			mails = (data.get("data") or {}).get("emails") or []
			logger.info(
				"验证码轮询: email=%s attempt=%d count=%d baseline=%d",
				email,
				attempt,
				len(mails),
				initial_count,
			)
			if not initialized_baseline and initial_count > 0:
				for item in mails[:initial_count]:
					subject = item.get("subject") or "-"
					contents = _mail_contents(item)
					code = None
					for raw in contents:
						code = _extract_code_from_email(raw)
						if code:
							break
					mail_ts = _mail_timestamp(item)
					if code and (mail_ts is None or mail_ts >= fresh_cutoff):
						logger.info("基线邮件命中验证码: subject=%s code=%s", subject, code)
						return code
					seen_mails.add(_mail_identity(item))
				initialized_baseline = True

			for item in mails:
				mail_id = _mail_identity(item)
				if mail_id in seen_mails:
					continue
				seen_mails.add(mail_id)
				subject = item.get("subject") or "-"
				contents = _mail_contents(item)
				preview = contents[0] if contents else ""
				logger.info("新邮件: subject=%s preview=%s", subject, _mail_preview(preview))
				code = None
				for raw in contents:
					code = _extract_code_from_email(raw)
					if code:
						break
				if code:
					logger.info("验证码提取成功: %s", code)
					return code
		time.sleep(settings.mail_poll_interval_sec)
	raise TimeoutException("等待验证码超时")


def _input_email_and_continue(driver: Any, email: str) -> None:
	wait = WebDriverWait(driver, 30)
	email_input = wait.until(EC.visibility_of_element_located((By.XPATH, SCRIPT_EMAIL_XPATH)))
	email_input.click()
	email_input.clear()
	for ch in email:
		email_input.send_keys(ch)
		time.sleep(0.04)
	time.sleep(0.4)

	input_value = (email_input.get_attribute("value") or "").strip()
	if input_value != email:
		logger.warning("邮箱输入值不一致，尝试 JS 回填: expected=%s actual=%s", email, input_value)
		driver.execute_script(
			"""
arguments[0].value = arguments[1];
arguments[0].dispatchEvent(new Event('input', {bubbles: true}));
arguments[0].dispatchEvent(new Event('change', {bubbles: true}));
""",
			email_input,
			email,
		)
		time.sleep(0.3)
		input_value = (email_input.get_attribute("value") or "").strip()
	if input_value != email:
		raise RuntimeError(f"邮箱输入失败: expected={email}, actual={input_value}")
	logger.info("邮箱输入完成: %s", input_value)

	continue_btn = wait.until(EC.element_to_be_clickable((By.XPATH, SCRIPT_CONTINUE_XPATH)))
	driver.execute_script("arguments[0].click();", continue_btn)
	logger.info("已点击 Continue")


def page_has_signin_error(driver: Any) -> bool:
	try:
		current_url = str(driver.current_url).lower()
	except Exception:
		current_url = ""
	if "signin-error" in current_url:
		return True
	try:
		text = (driver.page_source or "").lower()
	except Exception:
		return False
	return "let's try something else" in text or "trouble retrieving the email address" in text


def _input_verification_code(driver: Any, code: str) -> None:
	wait = WebDriverWait(driver, 20)
	try:
		pin = wait.until(EC.presence_of_element_located((By.CSS_SELECTOR, "input[name='pinInput']")))
		pin.click()
		for ch in code:
			pin.send_keys(ch)
			time.sleep(0.08)
	except Exception:
		span = driver.find_element(By.CSS_SELECTOR, "span[data-index='0']")
		span.click()
		driver.switch_to.active_element.send_keys(code)

	try:
		vbtn = driver.find_element(By.XPATH, SCRIPT_VERIFY_XPATH)
		driver.execute_script("arguments[0].click();", vbtn)
	except Exception:
		for btn in driver.find_elements(By.TAG_NAME, "button"):
			text = (btn.text or "").lower()
			if "verify" in text or "验证" in text or "continue" in text:
				driver.execute_script("arguments[0].click();", btn)
				break


def _fill_full_name_if_needed(driver: Any, full_name: str) -> None:
	selectors = [
		"input[formcontrolname='fullName']",
		"input[placeholder='全名']",
		"input[autocomplete='name']",
	]
	for selector in selectors:
		try:
			name_input = WebDriverWait(driver, 5).until(
				EC.visibility_of_element_located((By.CSS_SELECTOR, selector))
			)
			name_input.click()
			name_input.clear()
			for ch in full_name:
				name_input.send_keys(ch)
				time.sleep(0.03)
			name_input.submit()
			return
		except Exception:
			continue


def _wait_credentials_ready(driver: Any, timeout_sec: int = 50) -> None:
	deadline = time.time() + timeout_sec
	while time.time() < deadline:
		if page_has_signin_error(driver):
			raise RuntimeError(f"页面进入 signin-error: url={driver.current_url}")
		snapshot = collect_authorization_and_ids(driver)
		if snapshot.get("config_id") and snapshot.get("csesidx"):
			logger.info(
				"注册凭据就绪: url=%s config=%s csesidx=%s auth=%s",
				snapshot.get("current_url"),
				snapshot.get("config_id"),
				snapshot.get("csesidx"),
				bool(snapshot.get("authorization")),
			)
			return
		logger.info(
			"等待登录态: url=%s auth=%s config=%s csesidx=%s",
			snapshot.get("current_url"),
			bool(snapshot.get("authorization")),
			bool(snapshot.get("config_id")),
			bool(snapshot.get("csesidx")),
		)
		time.sleep(2)
	raise TimeoutException("等待登录态凭据超时")


def register_one(settings: Settings) -> Dict[str, Any]:
	email = create_temp_email(settings)
	full_name = random.choice(NAMES)
	driver = _build_driver(settings)

	logger.info("开始注册: email=%s", email)
	try:
		driver.get(settings.login_url)
		time.sleep(2)
		_input_email_and_continue(driver, email)
		for _ in range(6):
			if page_has_signin_error(driver):
				raise RuntimeError(f"页面进入 signin-error: url={driver.current_url}")
			time.sleep(1)
		initial_count = get_email_count(settings, email)
		code = wait_verification_code(settings, email, initial_count=initial_count)
		_input_verification_code(driver, code)
		time.sleep(3)
		_fill_full_name_if_needed(driver, full_name)
		_wait_credentials_ready(driver, timeout_sec=50)
		payload = build_upload_payload(
			driver=driver,
			email=email,
			full_name=full_name,
			mail_provider="chatgpt",
			mail_password="",
			is_new=True,
		)
		logger.info("注册流程完成: email=%s", email)
		return payload
	except Exception as exc:
		_save_debug_artifact(driver, settings, "register_failed")
		raise RuntimeError(f"注册失败 email={email}: {exc}") from exc
	finally:
		try:
			driver.quit()
		except Exception:
			pass
