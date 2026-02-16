"""Gemini Business 注册机"""
import undetected_chromedriver as uc
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
from bs4 import BeautifulSoup
from urllib.parse import urlparse, parse_qs
from datetime import datetime
import time, random, json, os, requests

# 配置
TOTAL_ACCOUNTS = 8
MAIL_API = "https://mail.chatgpt.org.uk"
MAIL_KEY = "gpt-test"
OUTPUT_DIR = "gemini_accounts"
LOGIN_URL = "https://auth.business.gemini.google/login?continueUrl=https:%2F%2Fbusiness.gemini.google%2F&wiffid=CAoSJDIwNTlhYzBjLTVlMmMtNGUxZC1hY2JkLThmOGY2ZDE0ODM1Mg"

# XPath
XPATH = {
    "email_input": "/html/body/c-wiz/div/div/div[1]/div/div/div/form/div[1]/div[1]/div/span[2]/input",
    "continue_btn": "/html/body/c-wiz/div/div/div[1]/div/div/div/form/div[2]/div/button",
    "verify_btn": "/html/body/c-wiz/div/div/div[1]/div/div/div/form/div[2]/div/div[1]/span/div[1]/button",
}

NAMES = ["James Smith", "John Johnson", "Robert Williams", "Michael Brown", "William Jones",
         "David Garcia", "Mary Miller", "Patricia Davis", "Jennifer Rodriguez", "Linda Martinez"]

def log(msg, level="INFO"): print(f"[{level}] {msg}")

def create_email():
    """创建临时邮箱"""
    try:
        r = requests.get(f"{MAIL_API}/api/generate-email",
            headers={"X-API-Key": MAIL_KEY}, timeout=30)
        if r.status_code == 200 and r.json().get('success'):
            email = r.json()['data']['email']
            log(f"邮箱创建: {email}")
            return email
    except Exception as e:
        log(f"创建邮箱失败: {e}", "ERR")
    return None

def get_code(email, timeout=30):
    """获取验证码"""
    log(f"等待验证码 (最多{timeout}s)...")
    start = time.time()
    while time.time() - start < timeout:
        try:
            r = requests.get(f"{MAIL_API}/api/emails", params={"email": email},
                headers={"X-API-Key": MAIL_KEY}, timeout=30)
            if r.status_code == 200:
                emails = r.json().get('data', {}).get('emails', [])
                if emails:
                    html = emails[0].get('html_content') or emails[0].get('content', '')
                    soup = BeautifulSoup(html, 'html.parser')
                    span = soup.find('span', class_='verification-code')
                    if span:
                        code = span.get_text().strip()
                        if len(code) == 6:
                            log(f"验证码: {code}")
                            return code
        except: pass
        print(f"  等待中... ({int(time.time()-start)}s)", end='\r')
        time.sleep(3)
    log("验证码超时", "ERR")
    return None

def save_config(email, cookies, url):
    """保存配置"""
    os.makedirs(OUTPUT_DIR, exist_ok=True)
    parsed = urlparse(url)
    path_parts = url.split('/')
    config_id = None
    for i, p in enumerate(path_parts):
        if p == 'cid' and i+1 < len(path_parts):
            config_id = path_parts[i+1]
            break

    cookie_dict = {c['name']: c for c in cookies}
    ses_cookie = cookie_dict.get('__Secure-C_SES', {})

    data = {
        "id": email,
        "csesidx": parse_qs(parsed.query).get('csesidx', [None])[0],
        "config_id": config_id,
        "secure_c_ses": ses_cookie.get('value'),
        "host_c_oses": cookie_dict.get('__Host-C_OSES', {}).get('value'),
        "expires_at": datetime.fromtimestamp(ses_cookie.get('expiry', 0) - 43200).strftime('%Y-%m-%d %H:%M:%S') if ses_cookie.get('expiry') else None
    }

    with open(f"{OUTPUT_DIR}/{email}.json", 'w') as f:
        json.dump(data, f, indent=2, ensure_ascii=False)
    log(f"配置已保存: {email}.json")
    return data

def register(driver):
    """注册单个账号"""
    email = create_email()
    if not email: return None, False, None

    wait = WebDriverWait(driver, 60)

    # 1. 访问登录页
    driver.get(LOGIN_URL)
    time.sleep(5)

    # 2. 输入邮箱
    log("输入邮箱...")
    inp = wait.until(EC.visibility_of_element_located((By.XPATH, XPATH["email_input"])))
    inp.click(); time.sleep(0.3); inp.clear(); time.sleep(0.3)
    for c in email: inp.send_keys(c); time.sleep(0.05)
    log(f"邮箱: {email}, 实际值: {inp.get_attribute('value')}")
    time.sleep(1)

    # 3. 点击继续
    btn = wait.until(EC.element_to_be_clickable((By.XPATH, XPATH["continue_btn"])))
    driver.execute_script("arguments[0].click();", btn)
    log("点击继续")
    time.sleep(3)

    # 4. 获取验证码
    code = get_code(email)
    if not code: return email, False, None

    # 5. 输入验证码
    time.sleep(2)
    log(f"输入验证码: {code}")
    try:
        pin = wait.until(EC.presence_of_element_located((By.CSS_SELECTOR, "input[name='pinInput']")))
        pin.click(); time.sleep(0.2)
        for c in code: pin.send_keys(c); time.sleep(0.1)
    except:
        try:
            span = driver.find_element(By.CSS_SELECTOR, "span[data-index='0']")
            span.click(); time.sleep(0.3)
            driver.switch_to.active_element.send_keys(code)
        except Exception as e:
            log(f"验证码输入失败: {e}", "ERR")
            return email, False, None

    # 6. 点击验证
    time.sleep(1)
    try:
        vbtn = driver.find_element(By.XPATH, XPATH["verify_btn"])
        driver.execute_script("arguments[0].click();", vbtn)
    except:
        for btn in driver.find_elements(By.TAG_NAME, "button"):
            if '验证' in btn.text: driver.execute_script("arguments[0].click();", btn); break
    log("点击验证")
    time.sleep(5)

    # 7. 输入姓名
    try:
        name_inp = WebDriverWait(driver, 30).until(EC.visibility_of_element_located(
            (By.CSS_SELECTOR, "input[formcontrolname='fullName'], input[placeholder='全名'], input#mat-input-0")))
        name = random.choice(NAMES)
        name_inp.clear(); time.sleep(0.3)
        for c in name: name_inp.send_keys(c); time.sleep(0.03)
        log(f"姓名: {name}")
        from selenium.webdriver.common.keys import Keys
        name_inp.send_keys(Keys.ENTER)
    except Exception as e:
        log(f"姓名输入异常: {e}", "WARN")

    # 8. 等待进入工作台
    log("等待工作台...")
    time.sleep(10)
    for _ in range(30):
        if 'business.gemini.google' in driver.current_url and 'auth' not in driver.current_url:
            break
        time.sleep(2)
    time.sleep(3)

    # 9. 保存配置
    config = save_config(email, driver.get_cookies(), driver.current_url)
    log(f"注册成功: {email}")
    return email, True, config

def main():
    print(f"\n{'='*50}\nGemini Business 批量注册 - 共 {TOTAL_ACCOUNTS} 个\n{'='*50}\n")

    driver = uc.Chrome(version_main=144, options=uc.ChromeOptions(), use_subprocess=True)
    success, fail, accounts = 0, 0, []

    for i in range(TOTAL_ACCOUNTS):
        print(f"\n{'#'*40}\n注册 {i+1}/{TOTAL_ACCOUNTS}\n{'#'*40}\n")

        try:
            driver.current_url  # 检查driver是否有效
        except:
            driver = uc.Chrome(version_main=144, options=uc.ChromeOptions(), use_subprocess=True)

        try:
            email, ok, cfg = register(driver)
            if ok: success += 1; accounts.append((email, cfg))
            else: fail += 1
        except Exception as e:
            log(f"异常: {e}", "ERR"); fail += 1
            try: driver.quit()
            except: pass
            driver = uc.Chrome(version_main=144, options=uc.ChromeOptions(), use_subprocess=True)

        print(f"\n进度: {i+1}/{TOTAL_ACCOUNTS} | 成功: {success} | 失败: {fail}")

        if i < TOTAL_ACCOUNTS - 1:
            try: driver.delete_all_cookies()
            except: pass
            time.sleep(random.randint(3, 5))

    try: driver.quit()
    except: pass

    print(f"\n{'='*50}\n完成! 成功: {success}, 失败: {fail}\n配置保存在: {OUTPUT_DIR}/\n{'='*50}")

if __name__ == "__main__":
    main()