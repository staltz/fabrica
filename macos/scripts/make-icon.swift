// Renders Fabrica's app icon into an .iconset folder, for iconutil.
//
//   swift macos/scripts/make-icon.swift out/AppIcon.iconset
//
// The icon is the favicon's mark (docs/favicon.svg) on its dark ground,
// in the rounded square macOS icons use.

import AppKit

let out = URL(fileURLWithPath: CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : "AppIcon.iconset")
try FileManager.default.createDirectory(at: out, withIntermediateDirectories: true)

let ink = NSColor(red: 0x1b / 255, green: 0x1a / 255, blue: 0x18 / 255, alpha: 1)
let orange = NSColor(red: 0xff / 255, green: 0x5b / 255, blue: 0x14 / 255, alpha: 1)
let paper = NSColor(red: 0xf3 / 255, green: 0xf0 / 255, blue: 0xe9 / 255, alpha: 1)

func render(_ px: Int) -> Data {
    let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: px, pixelsHigh: px, bitsPerSample: 8,
                               samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                               colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
    let ctx = NSGraphicsContext.current!.cgContext
    let s = CGFloat(px) / 1024
    ctx.scaleBy(x: s, y: s)
    // Flip, so the favicon's y-down coordinates work as they are.
    ctx.translateBy(x: 0, y: 1024)
    ctx.scaleBy(x: 1, y: -1)

    // The macOS icon grid: an 824-point rounded square, centered.
    let tile = CGRect(x: 100, y: 100, width: 824, height: 824)
    let shape = NSBezierPath(roundedRect: tile, xRadius: 185, yRadius: 185)
    ctx.saveGState()
    ctx.setShadow(offset: CGSize(width: 0, height: 10), blur: 20, color: NSColor.black.withAlphaComponent(0.3).cgColor)
    ink.setFill()
    shape.fill()
    ctx.restoreGState()

    // A soft light from the top, like the system icons have.
    ctx.saveGState()
    shape.addClip()
    let gradient = NSGradient(starting: NSColor.white.withAlphaComponent(0.10), ending: NSColor.white.withAlphaComponent(0))!
    gradient.draw(in: tile, angle: 90)
    ctx.restoreGState()

    // The mark, cropped to its own box (32 × 39 in the favicon's grid).
    let unit: CGFloat = 13
    let mark = CGSize(width: 32 * unit, height: 39 * unit)
    let origin = CGPoint(x: tile.midX - mark.width / 2 + 0.5 * unit, y: tile.midY - mark.height / 2)
    func rect(_ x: CGFloat, _ y: CGFloat, _ w: CGFloat, _ h: CGFloat) -> CGRect {
        CGRect(x: origin.x + x * unit, y: origin.y + y * unit, width: w * unit, height: h * unit)
    }
    orange.setFill()
    NSBezierPath(rect: rect(0, 0, 32, 8)).fill()
    paper.setFill()
    NSBezierPath(rect: rect(0, 11, 11, 28)).fill()
    NSBezierPath(rect: rect(11, 19, 14, 8)).fill()

    NSGraphicsContext.restoreGraphicsState()
    return rep.representation(using: .png, properties: [:])!
}

for size in [16, 32, 128, 256, 512] {
    try render(size).write(to: out.appending(path: "icon_\(size)x\(size).png"))
    try render(size * 2).write(to: out.appending(path: "icon_\(size)x\(size)@2x.png"))
}
