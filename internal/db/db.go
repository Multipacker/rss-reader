package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)



type Database interface {
	Begin(context context.Context) (pgx.Tx, error)
	Exec(context context.Context, sql string, arguments ...any) (commandTag pgconn.CommandTag, err error)
	Query(context context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(context context.Context, sql string, args ...any) pgx.Row
}



func Query[T any](context context.Context, db Database, sql string, args ...any) ([]T, error) {
	rows, err := db.Query(context, sql, args...)
	if err != nil {
		return nil, err
	}

	values, err := pgx.CollectRows(rows, pgx.RowToStructByName[T])
	if err != nil {
		return nil, err
	}

	return values, err
}

func QueryOne[T any](context context.Context, db Database, sql string, args ...any) (T, error) {
	values, err := Query[T](context, db, sql, args...)
	if err != nil {
		var zero T
		return zero, err
	}
	if len(values) == 0 {
		var zero T
		return zero, pgx.ErrNoRows
	}

	return values[0], nil
}

func QueryLax[T any](context context.Context, db Database, sql string, args ...any) ([]T, error) {
	rows, err := db.Query(context, sql, args...)
	if err != nil {
		return nil, err
	}

	values, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[T])
	if err != nil {
		return nil, err
	}

	return values, err
}

func QueryOneLax[T any](context context.Context, db Database, sql string, args ...any) ([]T, error) {
	values, err := QueryLax[T](context, db, sql, args...)
	if err != nil {
		var zero T
		return zero, err
	}
	if len(values) == 0 {
		var zero T
		return zero, pgx.ErrNoRows
	}

	return values[0], nil
}
