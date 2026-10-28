package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"github.com/shopspring/decimal"
	"time"
)

type LedgerEntry struct{ ent.Schema }

func (LedgerEntry) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "ledger_entries"}}
}
func (LedgerEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("transaction_id").Immutable(), field.String("account").Immutable(), field.Int64("subject_id").Optional().Nillable().Immutable(), field.String("side").Immutable(), field.Other("amount", decimal.Zero).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)", dialect.SQLite: "text"}).Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}
