import AppKit
import SwiftUI

/// Fabrica's mark, the "F" of the favicon (docs/favicon.svg): a bar on top,
/// a stem, and a short arm. Drawn in code so it's crisp at every size.
enum Mark {
    static let ink = NSColor(red: 0x1b / 255, green: 0x1a / 255, blue: 0x18 / 255, alpha: 1)
    static let orange = NSColor(red: 0xff / 255, green: 0x5b / 255, blue: 0x14 / 255, alpha: 1)
    static let paper = NSColor(red: 0xf3 / 255, green: 0xf0 / 255, blue: 0xe9 / 255, alpha: 1)

    // The favicon's rects, in its 48×48 grid with y going down, cropped to
    // the mark: x 8…40, y 5…44.
    static let bar = CGRect(x: 0, y: 0, width: 32, height: 8)
    static let stem = CGRect(x: 0, y: 11, width: 11, height: 28)
    static let arm = CGRect(x: 11, y: 19, width: 14, height: 8)
    static let size = CGSize(width: 32, height: 39)

    /// The menu bar icon. The top bar is solid while the server runs, and
    /// an outline while it doesn't.
    static func menuBarImage(serverRunning: Bool) -> NSImage {
        let height: CGFloat = 16
        let scale = height / size.height
        let image = NSImage(size: CGSize(width: ceil(size.width * scale) + 2, height: height + 2), flipped: true) { _ in
            NSColor.black.setFill()
            NSColor.black.setStroke()
            let t = { (r: CGRect) in
                CGRect(x: 1 + r.minX * scale, y: 1 + r.minY * scale, width: r.width * scale, height: r.height * scale)
            }
            NSBezierPath(rect: t(stem)).fill()
            NSBezierPath(rect: t(arm)).fill()
            if serverRunning {
                NSBezierPath(rect: t(bar)).fill()
            } else {
                let path = NSBezierPath(rect: t(bar).insetBy(dx: 0.6, dy: 0.6))
                path.lineWidth = 1.2
                path.stroke()
            }
            return true
        }
        image.isTemplate = true
        return image
    }
}

/// The mark in brand colors, for the app's own views.
struct MarkView: View {
    var size: CGFloat = 28

    var body: some View {
        Canvas { ctx, canvas in
            let s = canvas.height / Mark.size.height
            let dx = (canvas.width - Mark.size.width * s) / 2
            func r(_ rect: CGRect) -> Path {
                Path(CGRect(x: dx + rect.minX * s, y: rect.minY * s, width: rect.width * s, height: rect.height * s))
            }
            ctx.fill(r(Mark.bar), with: .color(Color(nsColor: Mark.orange)))
            ctx.fill(r(Mark.stem), with: .color(.primary))
            ctx.fill(r(Mark.arm), with: .color(.primary))
        }
        .frame(width: size * Mark.size.width / Mark.size.height, height: size)
        .accessibilityHidden(true)
    }
}
