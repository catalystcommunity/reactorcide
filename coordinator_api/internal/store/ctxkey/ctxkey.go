// Package ctxkey provides the transaction context key used by the store packages.
package ctxkey

// TxContextKey is the type used for storing transactions in context.
type TxContextKey struct{}

// TxKey returns the context key for database transactions.
func TxKey() interface{} {
	return TxContextKey{}
}
