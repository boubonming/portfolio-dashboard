package importer

import (
	"os"
	"path/filepath"
	"testing"
)

const fixture = `### USD-denominated holdings

| Symbol | Description | Units | Entry price / cost basis | Approx. cost |
|---|---|---:|---:|---:|
| NVDA | NVIDIA | 7 | USD 211.235714 total-lot average | USD 1,478.65 |
| NVDA | NVIDIA, additional lot | 7.06216 | USD 60.44 | USD 426.84 |
| **NVDA total** |  | **14.06216** | **Blended USD 135.504571** | **USD 1,905.486950** |
| INTC | Intel | 10 | USD 86.486 | USD 864.86 |
| INTC | Intel, additional lot | 2 | USD 33.61 | USD 67.22 |
| **INTC total** |  | **12** | **Blended USD 77.673333** | **USD 932.08** |
| BTC | Bitcoin | approx. 0.003681615826824646 | Bought at BTC price USD 81,485.96 | USD 300.00 |

### MYR-denominated holdings

| Symbol | Description | Units | Total cost | Average cost |
|---|---|---:|---:|---:|
| 1155 | Maybank | 400 | MYR 3,924.00 | MYR 9.81 |
`

func TestPreviewReconcilesLotsAndAggregateRows(t *testing.T) {
	preview, err := PreviewMarkdown(fixture, "fixture.md")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Summary.HoldingRows != 6 || preview.Summary.LotsIncluded != 6 || preview.Summary.AggregateRows != 2 {
		t.Fatalf("summary = %+v", preview.Summary)
	}
	if preview.Summary.DistinctSymbols != 4 {
		t.Fatalf("distinct symbols = %d", preview.Summary.DistinctSymbols)
	}
	if preview.Lots[0].Symbol != "NVDA" || preview.Lots[0].Quantity != "7" || preview.Lots[0].UnitCost == nil || *preview.Lots[0].UnitCost != "211.235714" {
		t.Fatalf("first lot = %+v", preview.Lots[0])
	}
	if preview.Lots[2].Classification != "aggregate" || preview.Lots[2].Included {
		t.Fatalf("aggregate = %+v", preview.Lots[2])
	}
	if preview.Lots[6].Quantity != "0.003681615826824646" || preview.Lots[6].CostBasis == nil || *preview.Lots[6].CostBasis != "300.00" {
		t.Fatalf("BTC quantity=%q cost=%v", preview.Lots[6].Quantity, preview.Lots[6].CostBasis)
	}
	if preview.Lots[0].AcquisitionDate != nil {
		t.Fatal("fixture has no acquisition dates")
	}
}

func TestPreviewMalformedRowsAreExplicit(t *testing.T) {
	preview, err := PreviewMarkdown("### USD-denominated holdings\n\n| Symbol | Units | Cost |\n|---|---:|---:|\n| | bad | USD nope |\n", "bad.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Unsupported) != 1 {
		t.Fatalf("unsupported = %+v", preview.Unsupported)
	}
}

func TestPreviewFileIsDeterministicAndDoesNotMutateSource(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "Portfolio.md")
	if err := os.WriteFile(filename, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	one, err := PreviewFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	two, err := PreviewFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if one.SourceSHA256 != two.SourceSHA256 || len(one.Lots) != len(two.Lots) {
		t.Fatal("repeat preview was not deterministic")
	}
	after, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("source changed")
	}
}
