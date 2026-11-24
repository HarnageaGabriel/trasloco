package mapping

import "github.com/HarnageaGabriel/trasloco/internal/ir"

func init() { Register(permissionsMapper{}) }

// permissionsMapper owns the Permissions concern (emit-gha.md section 5.12).
// The top-level deny-all block is fixed scaffolding of the emitter (INV-02):
// the writer always renders permissions: {} attributed to the pipeline node,
// and the fragment merge rejects any top-level permission entry. The mapper
// therefore claims no node and contributes no fragment, so every disposition
// stays with the emitter and conservation is unaffected. Job-level grants
// (checkout baseline, approved widening) arrive with a later tranche.
type permissionsMapper struct{}

func (permissionsMapper) Concern() Concern { return Permissions }

func (permissionsMapper) Map(*ir.Pipeline, func(string) bool) Result { return Result{} }
