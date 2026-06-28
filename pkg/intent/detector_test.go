package intent

import "testing"

func TestDetect(t *testing.T) {
	cases := []struct {
		prompt string
		want   Kind
	}{
		// debugging (EN + PT)
		{"why does this throw a nil pointer panic?", KindDebugging},
		{"there's a bug in the sync engine", KindDebugging},
		{"fix the failing login flow", KindDebugging},
		{"o login quebrou depois do deploy", KindDebugging},
		{"esse endpoint não funciona mais", KindDebugging},
		{"investigate this stack trace", KindDebugging},
		{"got an exception when pushing", KindDebugging},
		{"por que o teste falha intermitente", KindDebugging},
		// test execution
		{"run the tests for the store package", KindTestExecution},
		{"go test ./... is green now", KindTestExecution},
		{"vamos rodar os testes de integração", KindTestExecution},
		{"add unit test coverage for the parser", KindTestExecution},
		{"make test passes locally", KindTestExecution},
		// release
		{"let's cut a release for v0.7.0", KindRelease},
		{"deploy the server to production", KindRelease},
		{"bump version and update the changelog", KindRelease},
		{"precisamos lançar a versão nova", KindRelease},
		{"publicar o release de hoje", KindRelease},
		// security
		{"is this endpoint vulnerable to sql injection?", KindSecurity},
		{"review the auth middleware for security holes", KindSecurity},
		{"don't log the credential token", KindSecurity},
		{"revisar a segurança do upload", KindSecurity},
		{"check for xss in the comment field", KindSecurity},
		// planning
		{"let's plan the next milestone", KindPlanning},
		{"break this down into smaller tasks", KindPlanning},
		{"what are the next steps for the roadmap", KindPlanning},
		{"como devemos planejar a migração", KindPlanning},
		{"quais os próximos passos do projeto", KindPlanning},
		// architecture decision
		{"should we use postgres or sqlite here?", KindArchitecture},
		{"what's the trade-off of this design the way it is", KindArchitecture},
		{"qual banco usar para o cache", KindArchitecture},
		{"discuss the architecture of the sync layer", KindArchitecture},
		{"qual a melhor abordagem aqui", KindArchitecture},
		// code change
		{"implement the artifact store", KindCodeChange},
		{"refactor the hook dispatch", KindCodeChange},
		{"add a helper to parse the config", KindCodeChange},
		{"renomear essa função para algo claro", KindCodeChange},
		{"criar um endpoint de health", KindCodeChange},
		{"escreva uma função de validação", KindCodeChange},
		// memory operation
		{"remember that we use pnpm not npm", KindMemoryOp},
		{"salva isso na memória do projeto", KindMemoryOp},
		{"what did we decide about auth?", KindMemoryOp},
		{"lembra qual servidor o aron usa", KindMemoryOp},
		{"anota essa decisão", KindMemoryOp},
		// unknown
		{"oi", KindUnknown},
		{"hi", KindUnknown},
		{"", KindUnknown},
		{"   ", KindUnknown},
		{"the weather is nice today", KindUnknown},
	}

	for _, c := range cases {
		got := Detect(c.prompt)
		if got.Kind != c.want {
			t.Errorf("Detect(%q) = %q (signals %v), want %q", c.prompt, got.Kind, got.Signals, c.want)
		}
		if c.want != KindUnknown && got.Confidence <= 0 {
			t.Errorf("Detect(%q) confidence = %v, want > 0", c.prompt, got.Confidence)
		}
		if c.want == KindUnknown && got.Confidence != 0 {
			t.Errorf("Detect(%q) unknown confidence = %v, want 0", c.prompt, got.Confidence)
		}
	}
}

func TestDetect_ConfidenceSaturates(t *testing.T) {
	// More distinct cues -> higher confidence.
	one := Detect("refactor this")
	many := Detect("refactor and rename and extract this function and modify the code")
	if many.Confidence <= one.Confidence {
		t.Errorf("more cues should raise confidence: one=%v many=%v", one.Confidence, many.Confidence)
	}
}

func TestDetect_SingleHighPriorityCueIsConfident(t *testing.T) {
	got := Detect("there is a security concern")
	if got.Kind != KindSecurity {
		t.Fatalf("kind = %q", got.Kind)
	}
	if got.Confidence < 0.5 {
		t.Errorf("single high-priority cue confidence = %v, want >= 0.5", got.Confidence)
	}
}
