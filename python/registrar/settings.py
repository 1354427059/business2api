import os
import socket
from dataclasses import dataclass


def _env_bool(name: str, default: bool) -> bool:
	value = os.getenv(name)
	if value is None:
		return default
	return value.strip().lower() in {"1", "true", "yes", "on"}


@dataclass
class Settings:
	# 服务配置
	app_host: str
	app_port: int
	log_level: str

	# Go 服务配置
	b2a_base_url: str
	b2a_api_key: str
	request_timeout_sec: int

	# 注册配置
	login_url: str
	mail_api: str
	mail_key: str
	mail_poll_interval_sec: int
	mail_poll_timeout_sec: int

	# Selenium 远程浏览器
	selenium_remote_url: str
	headless: bool

	# 调度配置
	enable_auto_register: bool
	enable_auto_refresh: bool
	register_interval_sec: int
	refresh_interval_sec: int
	register_max_workers: int
	refresh_batch_limit: int
	refresh_task_lease_sec: int
	credentials_wait_timeout_sec: int
	worker_id: str

	# 工件目录
	artifacts_dir: str


def _default_worker_id() -> str:
	host = socket.gethostname().strip() or "worker"
	return f"{host}-{os.getpid()}"


def load_settings() -> Settings:
	return Settings(
		app_host=os.getenv("APP_HOST", "0.0.0.0"),
		app_port=int(os.getenv("APP_PORT", "8090")),
		log_level=os.getenv("LOG_LEVEL", "INFO"),
		b2a_base_url=os.getenv("B2A_BASE_URL", "http://business2api:8000").rstrip("/"),
		b2a_api_key=os.getenv("B2A_API_KEY", "").strip(),
		request_timeout_sec=int(os.getenv("REQUEST_TIMEOUT_SEC", "30")),
		login_url=os.getenv(
			"LOGIN_URL",
			"https://auth.business.gemini.google/login?continueUrl=https:%2F%2Fbusiness.gemini.google%2F&wiffid=CAoSJDIwNTlhYzBjLTVlMmMtNGUxZC1hY2JkLThmOGY2ZDE0ODM1Mg",
		),
		mail_api=os.getenv("MAIL_API", "https://mail.chatgpt.org.uk").rstrip("/"),
		mail_key=os.getenv("MAIL_KEY", "").strip(),
		mail_poll_interval_sec=int(os.getenv("MAIL_POLL_INTERVAL_SEC", "3")),
		mail_poll_timeout_sec=int(os.getenv("MAIL_POLL_TIMEOUT_SEC", "30")),
		selenium_remote_url=os.getenv("SELENIUM_REMOTE_URL", "http://selenium:4444/wd/hub"),
		headless=_env_bool("HEADLESS", True),
		enable_auto_register=_env_bool("ENABLE_AUTO_REGISTER", True),
		enable_auto_refresh=_env_bool("ENABLE_AUTO_REFRESH", True),
		register_interval_sec=int(os.getenv("REGISTER_INTERVAL_SEC", "30")),
		refresh_interval_sec=int(os.getenv("REFRESH_INTERVAL_SEC", "20")),
		register_max_workers=int(os.getenv("REGISTER_MAX_WORKERS", "1")),
		refresh_batch_limit=int(os.getenv("REFRESH_BATCH_LIMIT", "20")),
		refresh_task_lease_sec=int(os.getenv("REFRESH_TASK_LEASE_SEC", "180")),
		credentials_wait_timeout_sec=int(os.getenv("CREDENTIALS_WAIT_TIMEOUT_SEC", "40")),
		worker_id=os.getenv("WORKER_ID", _default_worker_id()).strip() or _default_worker_id(),
		artifacts_dir=os.getenv("ARTIFACTS_DIR", "/tmp/registrar-artifacts"),
	)
