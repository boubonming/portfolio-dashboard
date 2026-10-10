CREATE UNIQUE INDEX IF NOT EXISTS idx_fx_directed_market_time
    ON fx_rates(base_currency, quote_currency, market_at);
CREATE TRIGGER IF NOT EXISTS fx_pair_orientation_before_insert
BEFORE INSERT ON fx_rates
WHEN EXISTS (
    SELECT 1 FROM fx_rates
    WHERE base_currency = NEW.quote_currency
      AND quote_currency = NEW.base_currency
)
BEGIN
    SELECT RAISE(ABORT, 'FX pair orientation is already established in reverse');
END;
