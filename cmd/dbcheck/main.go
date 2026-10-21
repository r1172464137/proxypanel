// dbcheck verifies only the explicitly provisioned test database endpoints.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

func main() {
	if err := check(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func check() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pgURL, err := url.Parse(os.Getenv("DATABASE_URL"))
	if err != nil || pgURL.Hostname() != "127.0.0.1" || pgURL.Port() != "15432" || pgURL.Path != "/proxypanel" {
		return fmt.Errorf("refusing non-project PostgreSQL endpoint")
	}
	redisURL, err := url.Parse(os.Getenv("REDIS_URL"))
	if err != nil || redisURL.Hostname() != "127.0.0.1" || redisURL.Port() != "16379" || redisURL.Path != "/0" {
		return fmt.Errorf("refusing non-project Redis endpoint")
	}
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("PostgreSQL authentication/connection failed; verify the SSH tunnel")
	}
	defer conn.Close(context.Background())
	var user, database string
	var superuser bool
	if err = conn.QueryRow(ctx, "SELECT current_user, current_database(), rolsuper FROM pg_roles WHERE rolname=current_user").Scan(&user, &database, &superuser); err != nil {
		return fmt.Errorf("PostgreSQL identity query failed")
	}
	if user != "proxypanel" || database != "proxypanel" || superuser {
		return fmt.Errorf("unexpected database identity or privileges")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("PostgreSQL begin failed")
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "CREATE TEMPORARY TABLE proxypanel_connection_probe (n integer) ON COMMIT DROP; INSERT INTO proxypanel_connection_probe VALUES (42)"); err != nil {
		return fmt.Errorf("PostgreSQL temporary write failed")
	}
	var n int
	if err = tx.QueryRow(ctx, "SELECT n FROM proxypanel_connection_probe").Scan(&n); err != nil || n != 42 {
		return fmt.Errorf("PostgreSQL temporary read mismatch")
	}
	if err = tx.Rollback(ctx); err != nil {
		return fmt.Errorf("PostgreSQL rollback failed")
	}
	fmt.Println("PASS PostgreSQL: authenticated non-superuser, temporary write/read and rollback")
	opts, err := redis.ParseURL(os.Getenv("REDIS_URL"))
	if err != nil {
		return fmt.Errorf("invalid Redis configuration")
	}
	r := redis.NewClient(opts)
	defer r.Close()
	if err = r.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("Redis authentication/connection failed; verify the SSH tunnel")
	}
	var bytes [16]byte
	if _, err = rand.Read(bytes[:]); err != nil {
		return fmt.Errorf("random test identifier generation failed")
	}
	key := "proxypanel:dbcheck:" + hex.EncodeToString(bytes[:])
	ok, err := r.SetNX(ctx, key, "verified", time.Minute).Result()
	if err != nil || !ok {
		return fmt.Errorf("Redis isolated write failed")
	}
	defer r.Del(context.Background(), key)
	if got, err := r.Get(ctx, key).Result(); err != nil || got != "verified" {
		return fmt.Errorf("Redis isolated read mismatch")
	}
	if err = r.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("Redis test cleanup failed (key expires in 60 seconds)")
	}
	fmt.Println("PASS Redis: authenticated ping, isolated expiring write/read/delete")
	return nil
}
