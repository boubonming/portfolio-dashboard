export default function App() {
  return (
    <main className="shell">
      <header className="brand">
        <span className="eyebrow">Private workspace</span>
        <h1>Portfolio Dashboard</h1>
      </header>
      <section className="notice" aria-labelledby="status-title">
        <p className="eyebrow">Import status</p>
        <h2 id="status-title">Portfolio import is awaiting approval</h2>
        <p>The authoritative Obsidian portfolio has not been changed or imported. Review the reconciliation preview before any ledger cutover.</p>
      </section>
    </main>
  )
}
