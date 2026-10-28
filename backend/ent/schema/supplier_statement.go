package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/shopspring/decimal"
	"time"
)

type SupplierStatement struct{ ent.Schema }

func (SupplierStatement) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "supplier_statements"}}
}
func (SupplierStatement) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("supplier_id").Immutable(), field.Time("month").SchemaType(map[string]string{dialect.Postgres: "date"}).Immutable(), field.String("currency").Default("USD").Immutable(), field.Int("version").Immutable(), field.Int64("cutoff_ledger_id").Immutable(),
		field.Other("opening_pending", decimal.Zero).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}).Immutable(), field.Other("opening_available", decimal.Zero).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}).Immutable(), field.Other("opening_reserved", decimal.Zero).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}).Immutable(), field.Other("closing_pending", decimal.Zero).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}).Immutable(), field.Other("closing_available", decimal.Zero).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}).Immutable(), field.Other("closing_reserved", decimal.Zero).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}).Immutable(), field.Other("earned_net", decimal.Zero).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}).Immutable(), field.Other("platform_fee", decimal.Zero).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}).Immutable(), field.Other("refund_net", decimal.Zero).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}).Immutable(), field.Other("payout_paid", decimal.Zero).SchemaType(map[string]string{dialect.Postgres: "numeric(20,8)"}).Immutable(), field.Time("created_at").Default(time.Now).Immutable(),
	}
}
func (SupplierStatement) Indexes() []ent.Index {
	return []ent.Index{index.Fields("supplier_id", "month").Unique(), index.Fields("supplier_id", "month", "version").Unique()}
}
