import SwiftUI

// The few building blocks every page shares, so they all read the same:
// a header (icon, title, subtitle, a status and a switch), a narrow centered
// column, quiet footnotes, status pills and chips.

/// Keeps a page's content in a readable column, centered, while its scroll
/// bar stays at the window's edge.
struct ReadableColumn: ViewModifier {
    var width: CGFloat = 620

    func body(content: Content) -> some View {
        GeometryReader { geo in
            content.contentMargins(.horizontal, max(16, (geo.size.width - width) / 2), for: .scrollContent)
        }
    }
}

extension View {
    func readableColumn(_ width: CGFloat = 620) -> some View { modifier(ReadableColumn(width: width)) }
}

/// The top of a page: what it is, how it's doing, and its main switch.
struct PageHeader<Title: View, Subtitle: View, Accessory: View>: View {
    var symbol: String
    @ViewBuilder var title: Title
    @ViewBuilder var subtitle: Subtitle
    @ViewBuilder var accessory: Accessory

    var body: some View {
        HStack(alignment: .top, spacing: 14) {
            Image(systemName: symbol)
                .font(.system(size: 19, weight: .medium))
                .foregroundStyle(.secondary)
                .frame(width: 42, height: 42)
                .background(.quaternary.opacity(0.6), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            VStack(alignment: .leading, spacing: 3) {
                title
                    .font(.title2.weight(.semibold))
                subtitle
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            HStack(spacing: 12) { accessory }
                .padding(.top, 8)
        }
        .padding(.top, 8)
        .padding(.bottom, 22)
        .textCase(nil)
        .foregroundStyle(.primary)
    }
}

/// A small status: a colored glyph and a word or two.
struct StatusPill: View {
    var text: String
    var color: Color
    var symbol: String = "circle.fill"

    var body: some View {
        HStack(spacing: 5) {
            Image(systemName: symbol)
                .font(.system(size: symbol == "circle.fill" ? 6 : 10, weight: .bold))
            Text(text)
                .font(.caption.weight(.medium))
                .monospacedDigit()
        }
        .foregroundStyle(color)
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(color.opacity(0.12), in: Capsule())
        .fixedSize()
        .accessibilityElement(children: .combine)
    }
}

extension HomeRepo.Health {
    var label: String {
        switch self {
        case .inSync: "In sync"
        case .changes: "Changes to apply"
        case .errors: "Needs attention"
        case .off: "Off"
        case .unknown: "Checking"
        }
    }

    var color: Color {
        switch self {
        case .inSync: .green
        case .changes: .orange
        case .errors: .red
        case .off, .unknown: .secondary
        }
    }

    /// Shape as well as color, so status never depends on color alone.
    var symbol: String {
        switch self {
        case .inSync: "checkmark.circle.fill"
        case .changes: "arrow.triangle.2.circlepath.circle.fill"
        case .errors: "exclamationmark.triangle.fill"
        case .off: "pause.circle"
        case .unknown: "circle.dotted"
        }
    }
}

/// Secondary text under a form section, aligned with its rows.
struct Footnote<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 4) { content }
            .font(.callout)
            .foregroundStyle(.secondary)
            .multilineTextAlignment(.leading)
            .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// A problem worth reading first, at the top of a page.
struct Callout: View {
    var isError: Bool
    var message: String
    var hint: String?

    var body: some View {
        let color: Color = isError ? .red : .orange
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(systemName: isError ? "exclamationmark.octagon.fill" : "exclamationmark.triangle.fill")
                .foregroundStyle(color)
            VStack(alignment: .leading, spacing: 2) {
                Text(message)
                if let hint {
                    Text(hint).font(.caption).foregroundStyle(.secondary)
                }
            }
            .textSelection(.enabled)
            Spacer(minLength: 0)
        }
        .font(.callout)
        .padding(10)
        .background(color.opacity(0.08), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 8, style: .continuous).strokeBorder(color.opacity(0.18)))
        .textCase(nil)
    }
}

/// An on/off choice among several, like which tools a factory runs in.
struct Chip: View {
    var title: String
    @Binding var isOn: Bool
    var help: String?

    var body: some View {
        Button {
            isOn.toggle()
        } label: {
            HStack(spacing: 5) {
                Image(systemName: isOn ? "checkmark" : "plus")
                    .font(.system(size: 9, weight: .bold))
                    .opacity(isOn ? 1 : 0.55)
                Text(title)
            }
            .font(.callout.weight(isOn ? .medium : .regular))
            .padding(.horizontal, 10)
            .padding(.vertical, 5)
            .foregroundStyle(isOn ? AnyShapeStyle(.tint) : AnyShapeStyle(.secondary))
            .background(isOn ? AnyShapeStyle(.tint.opacity(0.14)) : AnyShapeStyle(.quaternary.opacity(0.5)), in: Capsule())
            .overlay(Capsule().strokeBorder(isOn ? AnyShapeStyle(.tint.opacity(0.35)) : AnyShapeStyle(.separator)))
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .help(help ?? "")
        .accessibilityAddTraits(isOn ? .isSelected : [])
    }
}

/// Lays children out in rows, wrapping when a row is full.
struct FlowLayout: Layout {
    var spacing: CGFloat = 6

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = arrange(subviews, width: proposal.width ?? .infinity)
        let height = rows.reduce(0) { $0 + $1.height } + spacing * CGFloat(max(0, rows.count - 1))
        let width = rows.map(\.width).max() ?? 0
        return CGSize(width: proposal.width ?? width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for row in arrange(subviews, width: bounds.width) {
            var x = bounds.minX
            for index in row.items {
                let size = subviews[index].sizeThatFits(.unspecified)
                subviews[index].place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
            y += row.height + spacing
        }
    }

    private struct Row { var items: [Int] = []; var width: CGFloat = 0; var height: CGFloat = 0 }

    private func arrange(_ subviews: Subviews, width: CGFloat) -> [Row] {
        var rows: [Row] = [Row()]
        for (i, view) in subviews.enumerated() {
            let size = view.sizeThatFits(.unspecified)
            let extra = rows[rows.count - 1].items.isEmpty ? size.width : rows[rows.count - 1].width + spacing + size.width
            if extra > width, !rows[rows.count - 1].items.isEmpty {
                rows.append(Row())
            }
            var row = rows[rows.count - 1]
            row.width = row.items.isEmpty ? size.width : row.width + spacing + size.width
            row.height = max(row.height, size.height)
            row.items.append(i)
            rows[rows.count - 1] = row
        }
        return rows
    }
}

/// A row action like "Add Repo": borderless, in the accent color.
struct AddButton: View {
    var title: String
    var action: () -> Void

    var body: some View {
        Button(action: action) {
            Label(title, systemImage: "plus")
                .font(.callout)
        }
        .buttonStyle(.borderless)
    }
}

/// A minus that only shows itself on hover, so lists stay quiet.
struct RemoveButton: View {
    var action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: "minus.circle.fill")
                .symbolRenderingMode(.hierarchical)
                .foregroundStyle(.secondary)
        }
        .buttonStyle(.borderless)
        .help("Remove")
        .accessibilityLabel("Remove")
    }
}

/// Monospaced output that follows the end as it grows.
struct LogView: View {
    var text: String
    var placeholder = "Nothing yet."
    /// Draws its own rounded box; off when it already sits in a form card.
    var framed = true

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                Text(text.isEmpty ? placeholder : text)
                    .font(.system(size: 11, design: .monospaced))
                    .lineSpacing(2)
                    .foregroundStyle(text.isEmpty ? .tertiary : .secondary)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(12)
                Color.clear.frame(height: 1).id("end")
            }
            .background(framed ? Color(nsColor: .textBackgroundColor).opacity(0.6) : .clear)
            .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 8, style: .continuous).strokeBorder(.separator.opacity(framed ? 0.6 : 0)))
            .onChange(of: text) { proxy.scrollTo("end", anchor: .bottom) }
            .onAppear { proxy.scrollTo("end", anchor: .bottom) }
        }
    }
}

/// "~/…" for paths in the home folder.
func abbreviatedPath(_ url: URL) -> String {
    let home = FileManager.default.homeDirectoryForCurrentUser.path(percentEncoded: false)
    let path = url.path(percentEncoded: false)
    return path.hasPrefix(home) ? "~/" + path.dropFirst(home.count) : path
}
