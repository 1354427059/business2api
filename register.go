package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

// ==================== 注册与刷新 ====================

var isRegistering int32
func startRegister(count int) error {
	if !atomic.CompareAndSwapInt32(&isRegistering, 0, 1) {
		return fmt.Errorf("注册进程已在运行")
	}

	// 使用配置文件中的脚本路径
	scriptPath := appConfig.Pool.RegisterScript
	if scriptPath == "" {
		scriptPath = "./main.js"
	}

	// 转换为绝对路径
	if !filepath.IsAbs(scriptPath) {
		absPath, err := filepath.Abs(scriptPath)
		if err == nil {
			scriptPath = absPath
		}
	}

	// 检查脚本是否存在
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		atomic.StoreInt32(&isRegistering, 0)
		return fmt.Errorf("注册脚本不存在: %s", scriptPath)
	}

	// 获取数据目录的绝对路径
	dataDirAbs, _ := filepath.Abs(DataDir)

	// 使用配置的线程数
	threads := appConfig.Pool.RegisterThreads
	if threads <= 0 {
		threads = 1
	}

	log.Printf("📝 启动 %d 个注册线程，目标: %d 个，当前: %d 个", threads, appConfig.Pool.TargetCount, pool.TotalCount())

	for i := 0; i < threads; i++ {
		go registerWorker(i+1, scriptPath, dataDirAbs)
	}
	go func() {
		for {
			time.Sleep(10 * time.Second)
			pool.Load(DataDir)
			if pool.TotalCount() >= appConfig.Pool.TargetCount {
				log.Printf("✅ 已达到目标账号数: %d，停止注册", pool.TotalCount())
				atomic.StoreInt32(&isRegistering, 0)
				return
			}
		}
	}()

	return nil
}

func registerWorker(id int, scriptPath, dataDirAbs string) {
	for atomic.LoadInt32(&isRegistering) == 1 {
		// 检查是否已达到目标
		if pool.TotalCount() >= appConfig.Pool.TargetCount {
			return
		}

		log.Printf("[注册线程 %d] 启动注册任务", id)

		args := []string{scriptPath, "--threads", "1", "--data-dir", dataDirAbs}
		if appConfig.Pool.RegisterHeadless {
			args = append(args, "--headless")
		}

		cmd := exec.Command("node", args...)
		cmd.Dir = filepath.Dir(scriptPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			log.Printf("[注册线程 %d] ⚠️ 执行失败: %v", id, err)
		}

		// 重新加载账号池
		pool.Load(DataDir)

		// 短暂延迟后继续
		time.Sleep(time.Second)
	}
	log.Printf("[注册线程 %d] 停止", id)
}

func poolMaintainer() {
	interval := time.Duration(appConfig.Pool.CheckIntervalMinutes) * time.Minute
	if interval < time.Minute {
		interval = 30 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	checkAndMaintainPool()

	for range ticker.C {
		checkAndMaintainPool()
	}
}

func checkAndMaintainPool() {
	pool.Load(DataDir)

	readyCount := pool.ReadyCount()
	pendingCount := pool.PendingCount()
	totalCount := pool.TotalCount()

	log.Printf("📊 号池检查: ready=%d, pending=%d, total=%d, 目标=%d, 最小=%d",
		readyCount, pendingCount, totalCount, appConfig.Pool.TargetCount, appConfig.Pool.MinCount)

	if totalCount < appConfig.Pool.TargetCount {
		needCount := appConfig.Pool.TargetCount - totalCount
		log.Printf("⚠️ 账号数未达目标，需要注册 %d 个", needCount)
		if err := startRegister(needCount); err != nil {
			log.Printf("❌ 启动注册失败: %v", err)
		}
	}
}
