package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"time"
)

type SupplierPayoutDestination struct{ ent.Schema }

func (SupplierPayoutDestination) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "supplier_payout_destinations"}}
}
func (SupplierPayoutDestination) Fields() []ent.Field {
	return []ent.Field{field.Int64("supplier_id"), field.String("masked_label"), field.Int64("version"), field.JSON("credential", map[string]any{}), field.Bool("verified").Default(false), field.Int64("verified_by").Optional().Nillable(), field.Time("verified_at").Optional().Nillable(), field.String("verification_evidence").Optional().Nillable(), field.Time("created_at").Default(time.Now).Immutable()}
}

func (SupplierPayoutDestination) Indexes() []ent.Index {
	return []ent.Index{index.Fields("supplier_id", "version").Unique()}
}
