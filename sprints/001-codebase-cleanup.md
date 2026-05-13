# Sprint 001 — Codebase Cleanup & Quality

## Metadados
- **Prioridade**: 🔴 Alta
- **Complexidade**: ~40 horas estimadas
- **Tags**: refactor, bugfix, testing, chore
- **Branch Git**: refactor/sprint-001-codebase-cleanup
- **Data Criação**: 2026-05-13
- **Data Início**: 2026-05-13

## Objetivo

Reduzir débito técnico identificado no codebase report. Organizado em 3 times que trabalham em paralelo sem conflitos de arquivo.

---

## Times

### Time A — Foundation (pkg/util + dead code)
**Escopo**: Novo pacote + remoções. Sem conflito com outros times.
**Estimativa**: 1-2 dias

- [ ] **A1**: Criar `pkg/util/uuid.go` com `NewID() string` que checa erro do `rand.Read` (panic on failure)
- [ ] **A2**: Criar `pkg/util/hash.go` com `ContentHash(string) string` e `FileHash(string) (string, error)`
- [ ] **A3**: Criar `pkg/util/similarity.go` com `CosineSimilarity(a, b []float32) float64`
- [ ] **A4**: Criar `pkg/util/logger.go` com `DefaultLogger(*slog.Logger) *slog.Logger`
- [ ] **A5**: Migrar callers de `newUUID/newID` em: `sqlite_store.go`, `session/manager.go`, `kg/kg.go`, `context/store.go`, `project/detector.go`, `importer/importer.go`
- [ ] **A6**: Migrar callers de content hash em: `sqlite_store.go`, `claude_code.go`, `indexer.go`, `embedding_cache.go`, `memory/indexer.go`
- [ ] **A7**: Migrar callers de cosine similarity em: `dream/analyzer.go`, `topic_change_detector.go`
- [ ] **A8**: Remover dead code:
  - `SaveMemory()` em `pkg/memory/service.go:280`
  - `toolCall` type + `extractToolCalls()` em `pkg/importer/claude_code.go:455-481`
  - `FindTool()` em `pkg/mcp/tools.go:467-474`
  - `TruncateAtBoundary()` em `pkg/memory/text_util.go:9-32`
  - `snapshotAge()` em `pkg/memory/entity_detector.go:221-228`
  - `processAttachmentEntry()` em `pkg/importer/claude_code.go:348`
  - `LegacyModelName` em `pkg/memory/embedding_cache.go:86` (usar o de `embeddings_onnx.go`)
- [ ] **A9**: Eliminar `float32SliceToBytes` em `embedding_cache.go` (usar `float32sToBlob` de `vector_cache.go`)
- [ ] **A10**: Consolidar `scanMemory` / `scanMemoryRow` em `sqlite_store.go` (extrair helper com interface `RowScanner`)
- [ ] **A11**: Eliminar `expandHome` duplicado entre `config.go` e `debuglog.go` (mover para `pkg/util/path.go`)
- [ ] **A12**: Substituir Newton sqrt em `dream/analyzer.go:269-278` por `math.Sqrt`
- [ ] **A13**: `go test ./...` passando

### Time B — Bug Fixes + Config Cleanup
**Escopo**: Fixes pontuais + config. Não toca nos arquivos que o Time A refatora.
**Estimativa**: 1 dia

- [x] **B1**: Corrigir `ExecuteFile` ignora timeout — `pkg/mcp/server_ctx.go:42` (`_ = timeoutSec` deve ser respeitado)
- [x] **B2**: Adicionar validação de `entityName` vazio em `kg/kg.go:99` antes da query
- [x] **B3**: Padronizar `Found` counter nos importers — DevClaw conta antes do save, outros depois
- [x] **B4**: Adicionar checagem de erro em `json.Unmarshal` de metadata em `sqlite_store.go:218,428,459`
- [x] **B5**: Adicionar checagem de erro em `json.Marshal` de keywords em `sqlite_store.go:521`
- [x] **B6**: Adicionar limite (LRU ou max-size) ao Fetcher cache em `pkg/context/fetcher.go:27`
- [x] **B7**: Limpar config fields fantasmas:
  - `Sanitizer.Patterns` — documentado com TODO (nunca lido pelo Sanitizer)
  - `Indexer.Enabled/Paths/Interval` — já conectados em `cmd/anchored/serve.go` e `import_cmd.go`
  - `Embedding.Provider/Model/Quantize` — documentados com TODO (nunca lidos pelo ONNX embedder); `Dimensions` é usado por `doctor.go`
- [x] **B8**: `go test ./...` passando

### Time C — Test Coverage (fase 2, depende de A)
**Escopo**: Testes para módulos críticos sem cobertura. Roda após Time A concluir migrações.
**Estimativa**: 3-5 dias

- [ ] **C1**: `pkg/util/*_test.go` — testes para NewID, ContentHash, CosineSimilarity, DefaultLogger
- [ ] **C2**: `pkg/memory/service_test.go` — Save, SaveWithOptions, embedAsync, BackfillEmbeddings (com mock Store/Embedder)
- [ ] **C3**: `pkg/memory/sqlite_store_test.go` — CRUD operations, scanMemory, ListWithoutEmbedding, UpdateEmbedding
- [ ] **C4**: `pkg/kg/kg_test.go` — AddTriple, QueryEntity, alias resolution, bitemporal logic
- [ ] **C5**: `pkg/mcp/server_test.go` — tool routing, parameter parsing, response formatting (pelo menos os 5 tools principais: context, search, save, execute, kg_query)
- [ ] **C6**: `pkg/importer/importer_test.go` — RunAll orchestration, error handling, dedup
- [ ] **C7**: `pkg/config/config_test.go` — YAML loading, path expansion, defaults
- [ ] **C8**: `go test ./...` passando com coverage report

---

## Dependências

```
Time A (foundation) ──→ Time C (tests)
Time B (bugfixes)       (independente, roda em paralelo com A)
```

- Time A e Time B podem rodar 100% em paralelo (sem overlap de arquivos)
- Time C depende de Time A concluir (testa a nova `pkg/util/`)
- Time C pode começar testes de `pkg/mcp/server.go` e `pkg/kg/kg.go` imediatamente (não dependem de A)

## Critérios de Aceite

- [ ] `make test` passa com zero falhas
- [ ] `make lint` sem novos warnings
- [ ] `make build` compila sem erros
- [ ] Zero duplicação de UUID/hash/similarity/logger-guard
- [ ] Dead code removido (zero funções sem callers)
- [ ] Bug do ExecuteFile timeout corrigido
- [ ] Coverage mínima de 60% nos packages críticos (memory, mcp, kg)

## Notas

- Sprint gerado a partir do codebase report em `~/.agent/diagrams/anchored-codebase-report.html`
- O fix de batch ONNX (embeddings_onnx.go) já foi implementado antes deste sprint
- Renomear `pkg/context/` ficou de fora deste sprint — é breaking change grande, merece sprint separado
