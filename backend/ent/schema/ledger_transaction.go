package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"time"
)

type LedgerTransaction struct{ ent.Schema }

func (LedgerTransaction) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "ledger_transactions"}}
}
func (LedgerTransaction) Fields() []ent.Field {
	return []ent.Field{
		field.String("operation_id").Unique().Immutable(), field.String("actor").Immutable(), field.String("fingerprint").Immutable(), field.String("currency").Immutable(), field.Int64("original_transaction_id").Optional().Nillable().Immutable(), field.Bool("sealed").Default(false),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("booked_at").Default(time.Now).Immutable().SchemaType(map[string]string{dialect.Postgres:"timestamptz"}),
	}
}
