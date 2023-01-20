package sql

import (
	"testing"

	pf "github.com/estuary/flow/go/protocols/flow"
	"github.com/stretchr/testify/require"
)

func TestAsFlatType(t *testing.T) {
	tests := []struct {
		name      string
		inference pf.Inference
		flatType  FlatType
		mustExist bool
	}{
		{
			name: "integer formatted string with integer",
			inference: pf.Inference{
				Exists: pf.Inference_MUST,
				Types:  []string{"integer", "string"},
				String_: &pf.Inference_String{
					Format: "integer",
				},
			},
			flatType:  INTEGER,
			mustExist: true,
		},
		{
			name: "number formatted string with number",
			inference: pf.Inference{
				Exists: pf.Inference_MAY,
				Types:  []string{"number", "string"},
				String_: &pf.Inference_String{
					Format: "number",
				},
			},
			flatType:  NUMBER,
			mustExist: false,
		},
		{
			name: "integer formatted string with number",
			inference: pf.Inference{
				Exists: pf.Inference_MAY,
				Types:  []string{"number", "string"},
				String_: &pf.Inference_String{
					Format: "integer",
				},
			},
			flatType:  MULTIPLE,
			mustExist: false,
		},
		{
			name: "single number type",
			inference: pf.Inference{
				Exists: pf.Inference_MAY,
				Types:  []string{"number"},
			},
			flatType:  NUMBER,
			mustExist: false,
		},
		{
			name: "number formatted string with number and other field",
			inference: pf.Inference{
				Exists: pf.Inference_MAY,
				Types:  []string{"number", "string", pf.JsonTypeArray},
				String_: &pf.Inference_String{
					Format: "number",
				},
			},
			flatType:  MULTIPLE,
			mustExist: false,
		},
		{
			name: "number formatted string with integer",
			inference: pf.Inference{
				Exists: pf.Inference_MAY,
				Types:  []string{"integer", "string"},
				String_: &pf.Inference_String{
					Format: "number",
				},
			},
			flatType:  MULTIPLE,
			mustExist: false,
		},
		{
			name: "multiple types with null",
			inference: pf.Inference{
				Exists: pf.Inference_MUST,
				Types:  []string{"integer", "string", pf.JsonTypeNull},
			},
			flatType:  MULTIPLE,
			mustExist: false,
		},
		{
			name: "no types",
			inference: pf.Inference{
				Exists: pf.Inference_MUST,
				Types:  nil,
			},
			flatType:  NEVER,
			mustExist: false,
		},
		{
			name: "no types with format",
			inference: pf.Inference{
				Exists: pf.Inference_MUST,
				Types:  nil,
				String_: &pf.Inference_String{
					Format: "number",
				},
			},
			flatType:  NEVER,
			mustExist: false,
		},
		{
			name: "other formatted string with integer",
			inference: pf.Inference{
				Exists: pf.Inference_MAY,
				Types:  []string{"integer", "string"},
				String_: &pf.Inference_String{
					Format: pf.JsonTypeArray,
				},
			},
			flatType:  MULTIPLE,
			mustExist: false,
		},
		{
			name: "format with two non-string fields",
			inference: pf.Inference{
				Exists: pf.Inference_MAY,
				Types:  []string{"integer", "number"},
				String_: &pf.Inference_String{
					Format: "number",
				},
			},
			flatType:  MULTIPLE,
			mustExist: false,
		},
		{
			name: "format with two string fields",
			inference: pf.Inference{
				Exists: pf.Inference_MAY,
				Types:  []string{"string", "string"},
				String_: &pf.Inference_String{
					Format: "number",
				},
			},
			flatType:  MULTIPLE,
			mustExist: false,
		},
		{
			name: "allowable format with a null is not mustExist",
			inference: pf.Inference{
				Exists: pf.Inference_MUST,
				Types:  []string{"string", pf.JsonTypeNull},
				String_: &pf.Inference_String{
					Format: "number",
				},
			},
			flatType:  STRING,
			mustExist: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projection := &Projection{
				Projection: pf.Projection{
					Inference: tt.inference,
				},
			}

			flatType, mustExist := projection.AsFlatType()
			require.Equal(t, tt.flatType, flatType)
			require.Equal(t, tt.mustExist, mustExist)
		})
	}
}