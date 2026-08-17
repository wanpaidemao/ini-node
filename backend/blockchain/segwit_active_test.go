package blockchain

import (
	"testing"

	"github.com/btcsuite/btcd/chaincfg/v2"
)

// TestSegwitForceActive isolates the ForceActive path: with
// AlwaysActiveHeight=1 the deploymentChecker must force segwit active at
// any height >= 1.  This tells us whether the config change actually
// takes effect vs. an ID/array mismatch.
func TestSegwitForceActive(t *testing.T) {
	params := chaincfg.SugarMainNetParams
	deployment := &params.Deployments[chaincfg.DeploymentSegwit]
	checker := deploymentChecker{deployment: deployment}

	// High tip height (like the real chain).
	node := &blockNode{height: 43900000}
	active := checker.ForceActive(node)
	t.Logf("AlwaysActiveHeight=%d EffectiveAlwaysActiveHeight=%d ForceActive(high)=%v",
		deployment.AlwaysActiveHeight, deployment.EffectiveAlwaysActiveHeight(), active)
	if !active {
		t.Errorf("ForceActive(high)=false, want true")
	}

	// Height 0 (genesis): still >= 1?  No, 0+1 >= 1 is true, so also active.
	genesis := &blockNode{height: 0}
	activeGen := checker.ForceActive(genesis)
	t.Logf("ForceActive(genesis)=%v", activeGen)

	// Nil node must be false (guard).
	if checker.ForceActive(nil) {
		t.Errorf("ForceActive(nil)=true, want false")
	}
}
