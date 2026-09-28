CREATE TABLE IF NOT EXISTS app.dashboards (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS app.dashboard_widgets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    dashboard_id UUID NOT NULL REFERENCES app.dashboards(id) ON DELETE CASCADE,
    widget_type TEXT NOT NULL CHECK (widget_type IN ('report', 'saved_query')),
    report_id UUID REFERENCES app.reports(id) ON DELETE SET NULL,
    saved_query_id UUID REFERENCES app.saved_queries(id) ON DELETE SET NULL,
    title TEXT,
    refresh_seconds INT NOT NULL DEFAULT 300,
    position INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_dashboard_widgets_dashboard ON app.dashboard_widgets(dashboard_id, position);
