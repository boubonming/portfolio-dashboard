(() => {
  const colors = ["#176b5d", "#b66b35", "#5677a9", "#9b568c", "#aa873d", "#71808e"];
  const $ = (id) => document.getElementById(id);
  const status = $("status");

  const formatMoney = (value, currency) => new Intl.NumberFormat(undefined, {
    style: "currency", currency, maximumFractionDigits: 2
  }).format(value);
  const formatNumber = (value) => new Intl.NumberFormat(undefined, { maximumFractionDigits: 4 }).format(value);
  const formatPct = (value) => `${value >= 0 ? "+" : ""}${value.toFixed(2)}%`;
  const formatDate = (value) => {
    const date = new Date(`${value}T00:00:00`);
    return Number.isNaN(date.valueOf()) ? value : new Intl.DateTimeFormat(undefined, {
      year: "numeric", month: "short", day: "numeric"
    }).format(date);
  };
  const signClass = (value) => value >= 0 ? "positive" : "negative";

  function render(data) {
    const { summary, currency, holdings, allocations, as_of: asOf } = data;
    $("as-of").textContent = `As of ${formatDate(asOf)}`;
    $("holding-count").textContent = `${holdings.length} ${holdings.length === 1 ? "holding" : "holdings"}`;
    $("total-market-value").textContent = formatMoney(summary.total_market_value, currency);
    $("total-cost-basis").textContent = formatMoney(summary.total_cost_basis, currency);
    $("gain-loss").textContent = formatMoney(summary.gain_loss, currency);
    $("gain-loss").className = signClass(summary.gain_loss);
    $("gain-loss-pct").textContent = `${formatPct(summary.gain_loss_pct)} overall return`;
    $("gain-loss-pct").className = `card-caption ${signClass(summary.gain_loss)}`;
    $("detail-date").textContent = formatDate(asOf);
    $("detail-currency").textContent = currency;
    $("detail-positions").textContent = String(holdings.length);
    $("table-summary").textContent = `${formatMoney(summary.total_market_value, currency)} total`;

    const legend = $("allocation-legend");
    legend.replaceChildren();
    const stops = [];
    let cursor = 0;
    allocations.forEach((allocation, index) => {
      const color = colors[index % colors.length];
      const end = cursor + allocation.percentage;
      stops.push(`${color} ${cursor}% ${end}%`);
      cursor = end;
      const item = document.createElement("li");
      item.innerHTML = `<span class="legend-dot" style="background:${color}"></span><span class="legend-name"></span><strong class="legend-value"></strong>`;
      item.querySelector(".legend-name").textContent = allocation.category;
      item.querySelector(".legend-value").textContent = `${allocation.percentage.toFixed(1)}%`;
      legend.append(item);
    });
    $("allocation-donut").style.background = allocations.length
      ? `conic-gradient(${stops.join(", ")})`
      : "conic-gradient(#dfe5ea 0 100%)";

    const body = $("holdings-body");
    body.replaceChildren();
    $("empty-state").hidden = holdings.length > 0;
    holdings.forEach((holding) => {
      const row = document.createElement("tr");
      const changeClass = signClass(holding.gain_loss);
      row.innerHTML = `<td><span class="holding-name"></span><span class="holding-symbol"></span></td>
        <td>${formatNumber(holding.quantity)}</td>
        <td>${formatMoney(holding.current_price, currency)}</td>
        <td>${formatMoney(holding.market_value, currency)}</td>
        <td><span class="${changeClass}">${formatMoney(holding.gain_loss, currency)}<br><small>${formatPct(holding.gain_loss_pct)}</small></span></td>
        <td>${holding.allocation_pct.toFixed(1)}%</td>`;
      row.querySelector(".holding-name").textContent = holding.name;
      row.querySelector(".holding-symbol").textContent = `${holding.symbol} · ${holding.category}`;
      body.append(row);
    });
    status.textContent = "Snapshot loaded locally.";
    window.setTimeout(() => { status.textContent = ""; }, 2500);
  }

  async function loadPortfolio() {
    try {
      const response = await fetch("/api/portfolio", { headers: { Accept: "application/json" } });
      if (!response.ok) throw new Error(`Request failed (${response.status})`);
      const data = await response.json();
      render(data);
    } catch (error) {
      status.className = "status status--error";
      status.textContent = `Unable to load portfolio: ${error.message}. Check the local server and try again.`;
      $("as-of").textContent = "Snapshot unavailable";
    }
  }

  loadPortfolio();
})();
