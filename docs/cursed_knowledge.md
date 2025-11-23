# Cursed Knowledge

Cursed knowledge we have learned as a result of building prep that we wish we never knew.

## Bubble Tea v2 Table

### The viewport needs a width

The `table.Model` in Bubble Tea v2 uses an internal viewport. If you don't set a width via `table.WithWidth()`, the viewport's `View()` returns an empty string because it checks `if w == 0 || h == 0 { return "" }`.

Your table will show the header but no rows.

### Height must be set after table creation

`table.WithHeight()` calculates the viewport height by subtracting the header height: `m.viewport.SetHeight(h - lipgloss.Height(m.headersView()))`. But if columns aren't set yet (options run in declaration order), `headersView()` returns an empty string with height 0, making your height calculation wrong.

Set height using `table.SetHeight()` after creating the table, not via `WithHeight()` option.