package memory_test

import (
	"testing"

	"github.com/gargi-indic/gargi-flow/internal/store/memory"
	"github.com/gargi-indic/gargi-flow/internal/store/storetest"
)

func TestPolicyStore(t *testing.T) {
	store := memory.NewPolicyStore()
	storetest.RunPolicyStoreTests(t, store)
}
