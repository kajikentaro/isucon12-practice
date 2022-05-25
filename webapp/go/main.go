package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/labstack/gommon/log"
	_ "github.com/mattn/go-sqlite3"
)

const (
	tenantDBSchemaFilePath = "../sql/20_schema_tenant.sql"
)

func getEnv(key string, defaultValue string) string {
	val := os.Getenv(key)
	if val != "" {
		return val
	}
	return defaultValue
}

func connectCenterDB() (*sqlx.DB, error) {
	config := mysql.NewConfig()
	config.Net = "tcp"
	config.Addr = getEnv("ISUCON_DB_HOST", "127.0.0.1") + ":" + getEnv("ISUCON_DB_PORT", "3306")
	config.User = getEnv("ISUCON_DB_USER", "isucon")
	config.Passwd = getEnv("ISUCON_DB_PASSWORD", "isucon")
	config.DBName = getEnv("ISUCON_DB_NAME", "isucon_listen80")
	config.ParseTime = true

	dsn := config.FormatDSN()
	return sqlx.Open("mysql", dsn)
}

func tenantDBPath(tenantID string) string {
	tenantDBDir := getEnv("ISUCON_TENANT_DB_DIR", "./tenants")
	return filepath.Join(tenantDBDir, tenantID+".db")
}

func connectTenantDB(tenantID string) (*sqlx.DB, error) {
	p := tenantDBPath(tenantID)
	return sqlx.Open("sqlite3", fmt.Sprintf("file:%s?mode=rw", p))
}

func createTenantDB(tenantID string) error {
	p := tenantDBPath(tenantID)

	cmd := exec.Command("sh", "-c", fmt.Sprintf("sqlite3 %s < %s", p, tenantDBSchemaFilePath))
	return cmd.Run()
}

func dispenseID(ctx context.Context) (int64, error) {
	ret, err := centerDB.ExecContext(ctx, "REPLACE INTO `id_generator` (`stub`) VALUES (?);", "a")
	if err != nil {
		return 0, fmt.Errorf("error REPLACE INTO `id_generator`: %w", err)
	}
	return ret.LastInsertId()
}

var centerDB *sqlx.DB

func main() {
	e := echo.New()
	e.Debug = true
	e.Logger.SetLevel(log.DEBUG)

	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	// for admin endpoint
	e.POST("/api/tenants/add", tenantsAddHandler)
	e.GET("/api/tenants/billing", tenantsBillingHandler)

	// for tenant endpoint
	// 参加者操作
	e.POST("/api/competitors/add", competitorsAddHandler)
	e.POST("/api/competitor/:competitior_id/disqualified", competitorsDisqualifiedHandler)
	// 大会操作
	e.POST("/api/competitions/add", competitionsAddHandler)
	e.POST("/api/competition/:competition_id/finish", competitionFinishHandler)
	e.POST("/api/competition/:competition_id/result", competitionResultHandler)
	// テナント操作
	e.GET("/api/tenant/billing", tenantBillingHandler)
	// 参加者からの閲覧
	e.GET("/api/competitor/:competitor_id", competitorHandler)
	e.GET("/api/competition/:competition_id/ranking", competitionRankingHandler)
	e.GET("/api/competitions", competitionsHandler)

	// for benchmarker
	e.POST("/initialize", initializeHandler)

	var err error
	centerDB, err = connectCenterDB()
	if err != nil {
		e.Logger.Fatalf("failed to connect db: %v", err)
		return
	}
	centerDB.SetMaxOpenConns(10)
	defer centerDB.Close()

	port := getEnv("SERVER_APP_PORT", "3000")
	e.Logger.Infof("starting isuports server on : %s ...", port)
	serverPort := fmt.Sprintf(":%s", port)
	e.Logger.Fatal(e.Start(serverPort))
}

func tenantsAddHandler(c echo.Context) error {
	// TODO: SaaS管理者かどうかをチェック
	name := c.FormValue("name")
	icon, err := c.FormFile("icon")
	if err != nil {
		return fmt.Errorf("error retrieve icon from FormFile: %w", err)
	}
	iconFile, err := icon.Open()
	if err != nil {
		return fmt.Errorf("error icon.Open: %w", err)
	}
	defer iconFile.Close()
	iconBytes, err := io.ReadAll(iconFile)
	if err != nil {
		return fmt.Errorf("error io.ReadAll: %w", err)
	}

	ctx := c.Request().Context()
	tx, err := centerDB.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("error centerDB.BeginTxx: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "LOCK TABLE `tenant` WRITE"); err != nil {
		tx.Rollback()
		return fmt.Errorf("error Lock table: %w", err)
	}
	id, err := dispenseID(ctx)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("error dispenseID: %w", err)
	}
	identifier := strconv.FormatInt(id, 10)
	now := time.Now()
	_, err = tx.ExecContext(
		ctx,
		"INSERT INTO `tenant` (`id`, `identifier`, `name`, `image`, `created_at`, `updated_at`)",
		id, identifier, name, iconBytes, now, now,
	)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("error Insert `tenant`: %w", err)
	}

	if err := createTenantDB(identifier); err != nil {
		tx.Rollback()
		return fmt.Errorf("error createTenantDB: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("error tx.Commit: %w", err)
	}
	return nil
}

type competitor struct {
	ID         int64
	Identifier string
	Name       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type competitorScore struct {
	CompetitionID        int64
	CompetitorIdentifier string
	Score                int64
}

func listScoresAll(ctx context.Context, tenantID string) ([]competitorScore, error) {
	tenantDB, err := connectTenantDB(tenantID)
	if err != nil {
		return nil, fmt.Errorf("error connectTenantDB: %w", err)
	}

	scores := []competitorScore{}
	rows, err := tenantDB.QueryxContext(
		ctx,
		"SELECT competition_id, competitor_id, score FROM competitor_score",
	)
	if err != nil {
		return nil, fmt.Errorf("error SELECT competitor_score: %w", err)
	}
	for rows.Next() {
		var competitionID, competitorID, score int64
		if err := rows.Scan(&competitionID, &competitorID, &score); err != nil {
			return nil, fmt.Errorf("error rows.Scan: %w", err)
		}
		var c competitor
		if err := tenantDB.GetContext(ctx, &c, "SELECT * FROM competitor WHERE id = ?", competitorID); err != nil {
			return nil, fmt.Errorf("erorr SELECT competitor: %w", err)
		}
		scores = append(scores, competitorScore{
			CompetitionID:        competitionID,
			CompetitorIdentifier: c.Identifier,
			Score:                score,
		})
	}

	return scores, nil
}

type successResult struct {
	Success bool `json:"status"`
	Data    any  `json:"data"`
}

type failureResult struct {
	Success bool   `json:"status"`
	Message string `json:"message"`
}

type tenantsBillingResult struct {
	Tenants []tenantBilling
}

type tenantBilling struct {
	TenantIdentifier string `json:"tenant_identifier"`
	TenantName       string `json:"tenant_name"`
	Billing          int64  `json:"billing"`
}

func tenantsBillingHandler(c echo.Context) error {
	ctx := c.Request().Context()
	// TODO: SaaS管理者かどうかをチェック

	// テナントごとに
	//   大会ごとに
	//     scoreに登録されているaccountでアクセスした人 * 100
	//     scoreに登録されているaccountでアクセスしていない人 * 50
	//     scoreに登録されていないaccountでアクセスした人 * 10
	//   を合計したものを
	// テナントの課金とする
	conn, err := centerDB.Connx(ctx)
	if err != nil {
		return fmt.Errorf("error centerDB.Conxx: %w", err)
	}
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `
CREATE TEMPORARY TABLE account_score (
	competition_id BIGINT UNSIGNED NOT NULL,
	account_identifier VARCAHR(191) NOT NULL,
	score BIGINT UNSIGNED NOT NULL
);
	`)
	if err != nil {
		return fmt.Errorf("error CREATE TEMPORARY TABLE account_score: %w", err)
	}
	tenantIDs := []string{}
	if err := conn.SelectContext(ctx, &tenantIDs, "SELECT id FROM tenant"); err != nil {
		return fmt.Errorf("error Select tenant: %w", err)
	}
	for _, tenantID := range tenantIDs {
		scores, err := listScoresAll(ctx, tenantID)
		if err != nil {
			return fmt.Errorf("error listScoresAll: %w", err)
		}
		for _, score := range scores {
			_, err := conn.ExecContext(
				ctx,
				"INSERT INTO account_score (competition_id, account_identifier, score) VALUES (?, ?, ?)",
				score.CompetitionID, score.CompetitorIdentifier, score.Score,
			)
			if err != nil {
				return fmt.Errorf("error INSERT account_score: %w", err)
			}
		}

	}
	tenantBillings := make([]tenantBilling, 0, len(tenantIDs))
	err = conn.SelectContext(ctx, &tenantBillings, `
WITH
q1 AS (
  SELECT
    tenant_id,
    competition_id,
    CASE account_access_log.id IS NULL WHEN 1 THEN 50 ELSE 100 END AS billing_scored,
    0 billing_accessed
  FROM account_score
  INNER JOIN account ON account_score.account_identifier = account.identifier
  LEFT OUTER JOIN account_access_log ON
    account_score.competition_id = account_access_log.competition_id AND
    account.account_account_id = account_access_log.account_id
  UNION ALL
  SELECT
    tenant_id,
    competition_id,
    0 AS billing_scored,
    10 AS billing_accessed
  FROM account_access_log
  INNER JOIN account ON account_access_log.account_id = account.id
),
q2 AS (
  SELECT tenant_id, competition_id,
  CASE SUM(billing_scored) > SUM(billing_accessed) WHEN 1 THEN SUM(billing_scored) ELSE SUM(billing_accessed) END AS billing
  GROUP BY tenant_id, competition_id
)
SELECT
  tenant.identifier AS tenant_identifier, tenant.name AS tenant_name, SUM(q1.billing)
FROM q2 JOIN tenant ON q1.tennant_id = tenant.id GROUP BY q1.tenant_id
	`)
	if err != nil {
		return fmt.Errorf("error retrieve tenantBillings: %w", err)
	}
	if err := c.JSON(http.StatusOK, successResult{
		Success: true,
		Data:    tenantBillings,
	}); err != nil {
		return fmt.Errorf("error c.JSON: %w", err)
	}
	return nil
}

func competitorsAddHandler(c echo.Context) error {
	// TODO: テナント管理者かチェック

	// 管理DBのaccountにinsert
	// テナントDBのcompetitorにinsert

	return nil
}

func competitorsDisqualifiedHandler(c echo.Context) error {
	// TODO: テナント管理者かチェック

	// 管理DBのaccountを`disqualified_competitor`にする
	return nil
}

func competitionsAddHandler(c echo.Context) error {
	// TODO: テナント管理者かチェック

	// テナントDBのcompetitionテーブルにinsert
	return nil
}

func competitionFinishHandler(c echo.Context) error {
	// TODO: テナント管理者かチェック

	// テナントDBのcompetitionテーブルのfinished_atを現在時刻を入れるようにupdate
	return nil
}

func competitionResultHandler(c echo.Context) error {
	// TODO: テナント管理者かチェック

	// アップロードされたCSVを読みながらテナントDBのcompetitor_scoreテーブルにループクエリでINSERT
	return nil
}

func tenantBillingHandler(c echo.Context) error {
	// TODO: テナント管理者かチェック

	// 大会ごとに
	//   scoreに登録されているaccountでアクセスした人 * 100
	//   scoreに登録されているaccountでアクセスしていない人 * 50
	//   scoreに登録されていないaccountでアクセスした人 * 10
	// を合計したものを計算する
	return nil
}

func competitorHandler(c echo.Context) error {
	// TODO: テナント管理者 or テナント参加者 or SaaS管理者かチェック
	// TODO: 失格者かチェック

	// テナントDBからcompetitorを取ってくる
	// テナントDBからcompetitor_idでcompetitor_scoreを取ってくる
	//    ループクエリでcompetitionを取ってくる
	return nil
}

func competitionRankingHandler(c echo.Context) error {
	// TODO: テナント管理者 or テナント参加者 or SaaS管理者かチェック
	// TODO: 失格者かチェック

	// テナントDBからcompetition_idでcompetitor_scoreを取ってくる
	//   ループクエリでテナントDBのcompetitorを引いて名前を埋め込む
	//   上から数えた順位を作成する。同じスコアなら同じ順位とする
	return nil
}

func competitionsHandler(c echo.Context) error {
	// TODO: テナント管理者 or テナント参加者 or SaaS管理者かチェック
	// TODO: 失格者かチェック

	// テナントDBからcompetition一覧を取ってくる
	return nil
}

func initializeHandler(c echo.Context) error {
	// TODO: SaaS管理者かチェック

	// constに定義されたmax_idより大きいIDのtenantを削除
	// constに定義されたmax_idより大きいIDのaccountを削除
	// constに定義されたmax_idより大きいIDのaccount_access_logを削除
	// constに定義されたmax_idにid_generatorを戻す
	// 残ったtenantのうち、max_idより大きいcompetition, competitor, competitor_scoreを削除

	return nil
}
