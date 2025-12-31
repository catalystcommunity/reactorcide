package cmd

import (
	"time"

	"github.com/catalystcommunity/app-utils-go/env"
	"github.com/catalystcommunity/app-utils-go/errorutils"
	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coredb"
	"github.com/pressly/goose/v3"
	"github.com/urfave/cli/v2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var migrations = coredb.Migrations

var MigrateCommand = &cli.Command{
	Name:  "migrate",
	Usage: "Runs database migrations",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:        "db-uri",
			Aliases:     []string{"db"},
			Value:       "postgresql://devuser:devpass@monodemo-postgresql:5432/monodemopg?sslmode=disable",
			Usage:       "The uri to use to connect to the db",
			Destination: &config.DbUri,
			EnvVars:     []string{"REACTORCIDE_DB_URI", "DB_URI"},
		},
	},
	Action: func(ctx *cli.Context) error {
		return RunMigrations()
	},
}

func RunMigrations() error {
	maxRetries := env.GetEnvAsIntOrDefault("DB_CONNECT_MAX_RETRIES", "30")
	retryInterval := time.Duration(env.GetEnvAsIntOrDefault("DB_CONNECT_RETRY_INTERVAL_SECONDS", "2")) * time.Second

	var db *gorm.DB
	var err error

	// Retry connection with backoff
	for attempt := 1; attempt <= maxRetries; attempt++ {
		db, err = gorm.Open(postgres.Open(config.DbUri), &gorm.Config{})
		if err == nil {
			break
		}
		if attempt == maxRetries {
			errorutils.LogOnErr(nil, "error opening database connection after retries", err)
			return err
		}
		logging.Log.WithError(err).Warnf("Database connection attempt %d/%d failed, retrying in %v", attempt, maxRetries, retryInterval)
		time.Sleep(retryInterval)
	}

	sqldb, err := db.DB()
	errorutils.LogOnErr(nil, "error getting database connection", err)
	if err != nil {
		return err
	}

	// Enable advisory locking for safe concurrent migrations
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		errorutils.LogOnErr(nil, "error setting goose dialect", err)
		return err
	}

	logging.Log.Info("Running migrations (with advisory lock)")
	err = goose.Up(sqldb, "migrations", goose.WithAllowMissing())
	errorutils.LogOnErr(nil, "error running migrations", err)
	if err != nil {
		return err
	}

	return nil
}
