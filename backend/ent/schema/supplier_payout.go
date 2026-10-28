package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"time"
)

type SupplierPayout struct{ ent.Schema }

func (SupplierPayout) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "supplier_payouts"}}
}
func (SupplierPayout) Fields() []ent.Field {
	return []ent.Field{field.Int64("supplier_id"), field.Int64("destination_id"), field.String("masked_label"), marketplaceAmount("amount"), field.String("currency").Default("USD"), field.String("status"), field.String("external_reference").Optional().Nillable().Unique(), field.String("evidence").Optional().Nillable(), field.Time("paid_at").Optional().Nillable(), field.Time("created_at").Default(time.Now).Immutable(), field.Time("updated_at").Default(time.Now)}
}

func (SupplierPayout) Indexes() []ent.Index { return []ent.Index{index.Fields("supplier_id")} }
