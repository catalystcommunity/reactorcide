package postgres_store

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/ctxkey"
	"gorm.io/gorm"
)

// InTransaction runs one storage unit in a database transaction. The callback
// receives a context that makes all PostgresDbStore operations use that
// transaction.
func (ps PostgresDbStore) InTransaction(ctx context.Context, fn func(context.Context) error) error {
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := context.WithValue(ctx, ctxkey.TxKey(), tx)
		return fn(txCtx)
	})
}
