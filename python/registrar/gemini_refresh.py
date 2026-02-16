import logging
import time
from typing import Any, Dict

from selenium.webdriver.common.by import By
from selenium.webdriver.support import expected_conditions as EC
from selenium.webdriver.support.ui import WebDriverWait

from collector import build_upload_payload, collect_authorization_and_ids
from gemini_register import (
	SCRIPT_CONTINUE_XPATH,
	SCRIPT_EMAIL_XPATH,
	SCRIPT_VERIFY_XPATH,
	_build_driver,
	get_email_count,
	page_has_signin_error,
	wait_verification_code,
)
from settings import Settings

logger = logging.getLogger(__name__)


def _input_email_and_continue(driver: Any, email: str) -> None:
	wait = WebDriverWait(driver, 30)
	email_input = wait.until(EC.visibility_of_element_located((By.XPATH, SCRIPT_EMAIL_XPATH)))
	email_input.click()
	email_input.clear()
	for ch in email:
		email_input.send_keys(ch)
		time.sleep(0.04)

	continue_btn = wait.until(EC.element_to_be_clickable((By.XPATH, SCRIPT_CONTINUE_XPATH)))
	driver.execute_script("arguments[0].click();", continue_btn)


def _input_code_and_verify(driver: Any, code: str) -> None:
	wait = WebDriverWait(driver, 20)
	try:
		pin = wait.until(EC.presence_of_element_located((By.CSS_SELECTOR, "input[name='pinInput']")))
		pin.click()
		pin.clear()
		for ch in code:
			pin.send_keys(ch)
			time.sleep(0.08)
	except Exception:
		span = driver.find_element(By.CSS_SELECTOR, "span[data-index='0']")
		span.click()
		driver.switch_to.active_element.send_keys(code)

	try:
		verify_btn = driver.find_element(By.XPATH, SCRIPT_VERIFY_XPATH)
		driver.execute_script("arguments[0].click();", verify_btn)
	except Exception:
		for btn in driver.find_elements(By.TAG_NAME, "button"):
			text = (btn.text or "").lower()
			if "verify" in text or "验证" in text or "continue" in text:
				driver.execute_script("arguments[0].click();", btn)
				break


def _wait_refresh_credentials_ready(driver: Any, timeout_sec: int = 40) -> None:
	deadline = time.time() + timeout_sec
	last_snapshot: Dict[str, str] = {}
	while time.time() < deadline:
		if page_has_signin_error(driver):
			raise RuntimeError(f"页面进入 signin-error: url={driver.current_url}")

		snapshot = collect_authorization_and_ids(driver)
		last_snapshot = snapshot
		has_auth = bool(snapshot.get("authorization"))
		has_config = bool(snapshot.get("config_id"))
		has_csesidx = bool(snapshot.get("csesidx"))
		logger.info(
			"等待续期凭据: url=%s auth=%s config=%s csesidx=%s source=%s",
			snapshot.get("current_url"),
			has_auth,
			has_config,
			has_csesidx,
			snapshot.get("authorization_source", ""),
		)
		if has_auth and has_config and has_csesidx:
			return
		time.sleep(2)

	if not last_snapshot.get("config_id") or not last_snapshot.get("csesidx"):
		raise RuntimeError("续期凭据不完整：缺少 config_id 或 csesidx")


def refresh_one(settings: Settings, task: Dict[str, Any]) -> Dict[str, Any]:
	email = str(task.get("email", "")).strip()
	if not email:
		raise ValueError("续期任务缺少 email")

	provider = str(task.get("mail_provider") or "chatgpt").strip().lower()
	if provider != "chatgpt":
		raise RuntimeError(f"暂不支持 provider={provider} 的自动续期")

	full_name = str(task.get("full_name", "")).strip()
	mail_password = str(task.get("mail_password", "")).strip()
	driver = _build_driver(settings)
	logger.info("开始续期: email=%s provider=%s", email, provider)

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
		_input_code_and_verify(driver, code)
		_wait_refresh_credentials_ready(driver, timeout_sec=settings.credentials_wait_timeout_sec)
		payload = build_upload_payload(
			driver=driver,
			email=email,
			full_name=full_name,
			mail_provider=provider,
			mail_password=mail_password,
			is_new=False,
		)
		logger.info(
			"续期成功: email=%s fallback=%s source=%s",
			email,
			payload.get("fallback_used", False),
			payload.get("authorization_source", ""),
		)
		return payload
	finally:
		try:
			driver.quit()
		except Exception:
			pass
