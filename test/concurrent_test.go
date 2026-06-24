package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Models
type User struct {
	ID             uint   `gorm:"primaryKey"`
	Name           string `gorm:"size:64;not null;uniqueIndex"`
	HashedPassword string `gorm:"not null"`
	Role           string `gorm:"type:varchar(16);not null"`
}

type Movie struct {
	ID          uint   `gorm:"primaryKey"`
	Title       string `gorm:"size:100;not null;uniqueIndex"`
	Description string `gorm:"type:text"`
}

type Showtime struct {
	ID      uint      `gorm:"primaryKey"`
	MovieID uint      `gorm:"not null;index"`
	StartAt time.Time `gorm:"not null"`
}

type Order struct {
	ID         uint `gorm:"primaryKey;autoIncrement:false"`
	ShowtimeID uint `gorm:"not null;index"`
	UserID     uint `gorm:"not null;index"`
}

type ReserveRequest struct {
	UserID     uint `json:"user_id"`
	ShowtimeID uint `json:"showtime_id"`
}

type TestResult struct {
	SuccessCount    int64
	SoldOutCount    int64
	AlreadyOrdered  int64
	OtherErrorCount int64
	TotalRequests   int64
	TotalDuration   time.Duration
	AvgResponseTime time.Duration
}

var ctx = context.Background()

func loadEnv() {
	dir, _ := os.Getwd()
	for {
		envPath := filepath.Join(dir, ".env")
		if _, err := os.Stat(envPath); err == nil {
			godotenv.Load(envPath)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}

func getBaseURL() string {
	if url := os.Getenv("BASE_URL"); url != "" {
		return url
	}
	return "http://localhost:4000"
}

var baseURL = getBaseURL()

func getDSN(key, defaultDB string) string {
	if dsn := os.Getenv(key); dsn != "" {
		return dsn
	}
	return fmt.Sprintf("host=localhost user=flash_sale_user password=flash-sale dbname=%s port=5432 sslmode=disable", defaultDB)
}

func setupTestDB(t *testing.T, userCount, showtimeCount, ticketCount int) (*gorm.DB, *gorm.DB) {
	loadEnv()

	reservationDB, err := gorm.Open(postgres.Open(getDSN("DATABASE_DSN", "reservation_db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to open reservation database: %v", err)
	}

	orderDB, err := gorm.Open(postgres.Open(getDSN("ORDER_DATABASE_DSN", "order_db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to open order database: %v", err)
	}

	// Clean and rebuild tables
	reservationDB.Migrator().DropTable(&Showtime{}, &Movie{}, &User{})
	reservationDB.AutoMigrate(&User{}, &Movie{}, &Showtime{})

	orderDB.Migrator().DropTable(&Order{})
	orderDB.AutoMigrate(&Order{})

	// Insert users
	users := make([]User, userCount)
	for i := 0; i < userCount; i++ {
		users[i] = User{
			Name:           fmt.Sprintf("用户%d", i+1),
			HashedPassword: fmt.Sprintf("pass%d", i+1),
			Role:           "user",
		}
	}
	if err := reservationDB.CreateInBatches(users, 1000).Error; err != nil {
		t.Fatalf("Failed to create users: %v", err)
	}

	movie := Movie{Title: "流浪地球3", Description: "科幻电影"}
	reservationDB.Create(&movie)

	for i := 1; i <= showtimeCount; i++ {
		showtime := Showtime{
			MovieID: 1,
			StartAt: time.Now().Add(time.Duration(i*2) * time.Hour),
		}
		reservationDB.Create(&showtime)
	}

	// Init Redis tickets
	cacheURL := os.Getenv("CACHE_URL")
	if cacheURL == "" {
		cacheURL = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: cacheURL, Password: "", DB: 0})
	rdb.FlushDB(ctx)

	initScript := redis.NewScript(`
for i = 1, #ARGV, 2 do
    local key = ARGV[i]
    local value = tonumber(ARGV[i + 1])
    redis.call("SET", key, value)
end
return #ARGV / 2
`)
	args := make([]any, 0, showtimeCount*2)
	for i := 1; i <= showtimeCount; i++ {
		args = append(args, fmt.Sprintf("showtime:%d:ticket:remain", i), ticketCount)
	}
	if _, err := initScript.Run(ctx, rdb, []string{}, args...).Result(); err != nil {
		t.Fatalf("Failed to init redis cache: %v", err)
	}

	t.Logf("测试数据初始化完成: %d个用户, %d个场次, 每场%d张票", userCount, showtimeCount, ticketCount)

	return reservationDB, orderDB
}

var httpClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        20000,
		MaxIdleConnsPerHost: 20000,
		MaxConnsPerHost:     20000,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	},
	Timeout: 5 * time.Second,
}

func sendReserveRequest(userID, showtimeID uint) (statusCode int, responseBody string, duration time.Duration, err error) {
	reqBody := ReserveRequest{UserID: userID, ShowtimeID: showtimeID}
	jsonData, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", baseURL+"/reserve", bytes.NewBuffer(jsonData))
	if err != nil {
		return 0, "", 0, err
	}

	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := httpClient.Do(req)
	duration = time.Since(start)

	if err != nil {
		return 0, "", duration, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), duration, nil
}

func concurrentTest(t *testing.T, concurrency int, showtimeID uint, userIDGenerator func(int) uint) *TestResult {
	result := &TestResult{}
	var wg sync.WaitGroup
	var totalDuration int64

	for i := range concurrency {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			userID := userIDGenerator(index)
			statusCode, body, duration, err := sendReserveRequest(userID, showtimeID)

			atomic.AddInt64(&totalDuration, int64(duration))
			atomic.AddInt64(&result.TotalRequests, 1)

			if err != nil {
				atomic.AddInt64(&result.OtherErrorCount, 1)
				t.Logf("请求错误 [用户%d]: %v", userID, err)
				return
			}

			switch statusCode {
			case 200:
				atomic.AddInt64(&result.SuccessCount, 1)
			case 409:
				if strings.Contains(body, "sold out") {
					atomic.AddInt64(&result.SoldOutCount, 1)
				} else if strings.Contains(body, "Already ordered") {
					atomic.AddInt64(&result.AlreadyOrdered, 1)
				} else {
					atomic.AddInt64(&result.OtherErrorCount, 1)
					t.Logf("409但非预期错误 [用户%d]: %s", userID, body)
				}
			default:
				atomic.AddInt64(&result.OtherErrorCount, 1)
				t.Logf("未预期状态码 [用户%d]: %d, 响应: %s", userID, statusCode, body)
			}
		}(i)
	}

	wg.Wait()
	result.TotalDuration = time.Since(time.Now().Add(-time.Duration(totalDuration / result.TotalRequests)))
	result.AvgResponseTime = time.Duration(totalDuration / result.TotalRequests)
	return result
}

func printTestResult(t *testing.T, scenarioName string, result *TestResult, totalDuration time.Duration) {
	t.Logf("\n============================================================")
	t.Logf("%s - 测试结果", scenarioName)
	t.Logf("============================================================")
	t.Logf("成功预订: %d", result.SuccessCount)
	t.Logf("已售罄: %d", result.SoldOutCount)
	t.Logf("重复预订: %d", result.AlreadyOrdered)
	t.Logf("其他错误: %d", result.OtherErrorCount)
	t.Logf("总请求数: %d", result.TotalRequests)
	t.Logf("总耗时: %v", totalDuration)
	t.Logf("平均响应时间: %v", result.AvgResponseTime)
	if totalDuration.Seconds() > 0 {
		t.Logf("QPS: %.2f", float64(result.TotalRequests)/totalDuration.Seconds())
	}
	t.Logf("============================================================\n")
}

func verifyOrderCount(t *testing.T, orderDB *gorm.DB, showtimeID uint, expectedCount int64) {
	var actualCount int64
	orderDB.Model(&Order{}).Where("showtime_id = ?", showtimeID).Count(&actualCount)

	if actualCount != expectedCount {
		t.Errorf("数据库订单数不一致！期望: %d, 实际: %d", expectedCount, actualCount)
	} else {
		t.Logf("数据库验证通过: %d 条订单", actualCount)
	}
}

// 场景1: 极限抢票测试（超卖验证）
func TestConcurrent_OversellPrevention(t *testing.T) {
	const (
		ticketCount = 100
		concurrency = 7000
		showtimeID  = 1
	)

	_, orderDB := setupTestDB(t, concurrency, 1, ticketCount)

	t.Logf("\n场景1: 极限抢票测试")
	t.Logf("票数: %d, 并发用户: %d", ticketCount, concurrency)

	startTime := time.Now()
	result := concurrentTest(t, concurrency, showtimeID, func(i int) uint {
		return uint(i + 1)
	})
	totalDuration := time.Since(startTime)

	printTestResult(t, "场景1: 超卖测试", result, totalDuration)

	if result.SuccessCount != ticketCount {
		t.Errorf("超卖检测失败！成功预订: %d, 实际票数: %d", result.SuccessCount, ticketCount)
	} else {
		t.Logf("超卖检测通过！")
	}

	expectedFailed := int64(concurrency - ticketCount)
	actualFailed := result.SoldOutCount + result.OtherErrorCount
	if actualFailed != expectedFailed {
		t.Errorf("失败数不符！期望: %d, 实际: %d", expectedFailed, actualFailed)
	}

	fmt.Printf("订票已完成，等待3秒保证数据库写入完成\n")
	time.Sleep(3 * time.Second)
	verifyOrderCount(t, orderDB, showtimeID, ticketCount)
}

// 场景2: 同一用户幂等性测试
func TestConcurrent_IdempotencyCheck(t *testing.T) {
	const (
		concurrency = 20
		showtimeID  = 1
		userID      = 1
	)

	_, orderDB := setupTestDB(t, 10, 1, 10)

	t.Logf("\n场景2: 同一用户幂等性测试")
	t.Logf("用户%d 发起 %d 个并发请求", userID, concurrency)

	startTime := time.Now()
	result := concurrentTest(t, concurrency, showtimeID, func(i int) uint {
		return userID
	})
	totalDuration := time.Since(startTime)

	printTestResult(t, "场景2: 幂等性测试", result, totalDuration)

	if result.SuccessCount != 1 {
		t.Errorf("幂等性检测失败！成功次数: %d, 期望: 1", result.SuccessCount)
	} else {
		t.Logf("幂等性检测通过！")
	}

	if result.AlreadyOrdered != int64(concurrency-1) {
		t.Errorf("重复预订错误数不符！期望: %d, 实际: %d", concurrency-1, result.AlreadyOrdered)
	}

	fmt.Printf("订票已完成，等待3秒保证数据库写入完成\n")
	time.Sleep(3 * time.Second)
	verifyOrderCount(t, orderDB, showtimeID, 1)
}

// 场景3: 多场次混合测试
func TestConcurrent_MultipleShowtimes(t *testing.T) {
	const (
		showtimeCount      = 3
		ticketsPerShowtime = 50
		totalConcurrency   = 3000
	)

	_, orderDB := setupTestDB(t, totalConcurrency, showtimeCount, ticketsPerShowtime)

	t.Logf("\n场景3: 多场次混合测试")
	t.Logf("%d个场次, 每场%d张票, 总并发: %d", showtimeCount, ticketsPerShowtime, totalConcurrency)

	var wg sync.WaitGroup
	results := make([]*TestResult, showtimeCount)

	overallStart := time.Now()
	for showtimeID := 1; showtimeID <= showtimeCount; showtimeID++ {
		wg.Add(1)
		go func(sid int) {
			defer wg.Done()
			concurrency := totalConcurrency / showtimeCount
			userOffset := (sid - 1) * concurrency
			results[sid-1] = concurrentTest(t, concurrency, uint(sid), func(i int) uint {
				return uint(userOffset + i + 1)
			})
		}(showtimeID)
	}
	wg.Wait()
	overallDuration := time.Since(overallStart)

	fmt.Printf("订票已完成，等待3秒保证数据库写入完成\n")
	time.Sleep(3 * time.Second)

	totalSuccess := int64(0)
	for i, result := range results {
		showtimeID := i + 1
		printTestResult(t, fmt.Sprintf("场景3-场次%d", showtimeID), result, overallDuration)
		totalSuccess += result.SuccessCount
		verifyOrderCount(t, orderDB, uint(showtimeID), result.SuccessCount)
	}

	t.Logf("\n多场次总结: 总成功预订 %d 笔", totalSuccess)
}
