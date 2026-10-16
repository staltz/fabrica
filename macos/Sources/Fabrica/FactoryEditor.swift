import AppKit
import SwiftUI

/// Edits one factory. Nothing is written until you save, and every save is
/// a commit in your home repo.
struct FactoryEditor: View {
    @Environment(AppModel.self) private var model

    /// The file being edited, or nil for a new factory.
    let entry: HomeRepo.Entry?

    @State private var draft: Factory
    @State private var original: Factory?
    @State private var kind: Kind
    @State private var saving = false
    @State private var saveError: String?
    @State private var confirmingDelete = false

    enum Kind: Hashable { case local, cloud }

    init(entry: HomeRepo.Entry?) {
        self.entry = entry
        let f = entry?.factory ?? Factory(name: "", runtimes: ["claude-code-local"], targets: [.init(repo: Factory.wildcard)])
        _draft = State(initialValue: f)
        _original = State(initialValue: entry?.factory)
        _kind = State(initialValue: f.isCloud ? .cloud : .local)
    }

    var body: some View {
        if let entry, entry.factory == nil {
            unreadable(entry)
        } else {
            form
        }
    }

    private var isNew: Bool { entry == nil }
    private var isDirty: Bool { original != draft }

    private var problems: [String] {
        var p: [String] = []
        if !Factory.isValidName(draft.name) { p.append("Name it with lowercase letters, digits and single hyphens, like pr-review.") }
        if draft.blueprint.git.trimmingCharacters(in: .whitespaces).isEmpty { p.append("Say which blueprint to use.") }
        if draft.blueprint.ref.trimmingCharacters(in: .whitespaces).isEmpty { p.append("Pick a branch, tag or commit of the blueprint.") }
        if draft.runtimes.isEmpty { p.append("Pick at least one tool to run in.") }
        if draft.targets.isEmpty || draft.targets.contains(where: { $0.repo.trimmingCharacters(in: .whitespaces).isEmpty }) {
            p.append("Add the repos this factory works on.")
        }
        if kind == .cloud {
            if draft.server.isEmpty { p.append("A cloud factory needs the address of its Fabrica server.") }
            if draft.routines.isEmpty { p.append("A cloud factory needs at least one routine.") }
        }
        return p
    }

    private var health: HomeRepo.Health {
        if !draft.enabled { return .off }
        if isNew { return .unknown }
        guard let entry else { return .unknown }
        return model.home.health(of: entry)
    }

    // MARK: Form

    private var form: some View {
        Form {
            blueprintSection
            runtimesSection
            targetsSection
            routinesSection
            paramsSection
            optionsSection
        }
        .formStyle(.grouped)
        .readableColumn()
        .toolbar {
            ToolbarItemGroup(placement: .confirmationAction) {
                if isDirty || isNew {
                    if !isNew {
                        Button("Revert") { if let original { draft = original; kind = original.isCloud ? .cloud : .local } }
                            .keyboardShortcut(.cancelAction)
                    }
                    Button(isNew ? "Create" : "Save") { save() }
                        .buttonStyle(.borderedProminent)
                        .keyboardShortcut("s")
                        .disabled(!problems.isEmpty || saving)
                        .help(problems.first ?? "Saves the factory, and commits it")
                }
            }
            if let entry {
                ToolbarItem(placement: .primaryAction) {
                    Menu {
                        Button("Duplicate") { duplicate() }
                        Button("Show in Finder") {
                            NSWorkspace.shared.activateFileViewerSelecting([model.home.factoriesURL.appending(path: entry.file)])
                        }
                        Divider()
                        Button("Delete…", role: .destructive) { confirmingDelete = true }
                    } label: {
                        Label("More", systemImage: "ellipsis")
                    }
                    .menuIndicator(.hidden)
                }
            }
        }
        .confirmationDialog("Delete \(draft.name)?", isPresented: $confirmingDelete) {
            Button("Delete", role: .destructive) {
                guard let entry else { return }
                Task {
                    await model.home.delete(entry)
                    model.selection = nil
                }
            }
        } message: {
            Text("This removes the factory and commits that. What apply installed stays until your next apply. To pause a factory instead, turn it off.")
        }
        .alert("Couldn't save", isPresented: .constant(saveError != nil)) {
            Button("OK") { saveError = nil }
        } message: {
            Text(saveError ?? "")
        }
    }

    // MARK: Header

    private var header: some View {
        VStack(alignment: .leading, spacing: 10) {
            PageHeader(symbol: kind == .local ? "laptopcomputer" : "cloud") {
                TextField("Name", text: $draft.name, prompt: Text("factory-name"))
                    .textFieldStyle(.plain)
                    .labelsHidden()
            } subtitle: {
                TextField("Description", text: $draft.notes, prompt: Text("Add a description"), axis: .vertical)
                    .textFieldStyle(.plain)
                    .labelsHidden()
                    .lineLimit(1...4)
            } accessory: {
                if !isNew && draft.enabled {
                    StatusPill(text: health == .changes ? changesText : health.label, color: health.color, symbol: health.symbol)
                }
                Toggle("On", isOn: $draft.enabled)
                    .toggleStyle(.switch)
                    .labelsHidden()
                    .controlSize(.small)
                    .help(draft.enabled ? "On. Turn it off and Fabrica ignores this factory." : "Off. Fabrica ignores this factory.")
            }
            if !draft.name.isEmpty && !Factory.isValidName(draft.name) {
                Callout(isError: true, message: "Use lowercase letters, digits and single hyphens, like pr-review.")
            }
            ForEach(entry == nil ? [] : model.home.diagnostics(for: draft.name)) { d in
                Callout(isError: d.isError, message: d.message, hint: d.hint)
            }
        }
        .padding(.bottom, 6)
    }

    private var changesText: String {
        let n = model.home.plans[draft.name]?.changes ?? 0
        return "\(n) change\(n == 1 ? "" : "s")"
    }

    // MARK: Sections

    private var blueprintSection: some View {
        Section {
            LabeledContent("Repository") {
                HStack(spacing: 6) {
                    TextField("Repository", text: $draft.blueprint.git, prompt: Text("github.com/you/blueprint"))
                        .labelsHidden()
                        .multilineTextAlignment(.trailing)
                    Button { chooseBlueprintFolder() } label: {
                        Image(systemName: "folder")
                    }
                    .buttonStyle(.borderless)
                    .help("Use a blueprint repo on this Mac")
                }
            }
            TextField("Branch, tag or commit", text: $draft.blueprint.ref, prompt: Text("main"))
            TextField("Folder", text: $draft.blueprint.path, prompt: Text("Repo root"))
        } header: {
            VStack(alignment: .leading, spacing: 0) {
                header
                Text("Blueprint")
            }
        } footer: {
            Footnote { Text(blueprintNote) }
        }
    }

    private var blueprintNote: String {
        let plan = model.home.plans[draft.name]
        let at = plan?.commit.map { " Now at \(String($0.prefix(7)))." } ?? ""
        guard let plan, !isDirty else {
            return "A branch follows the blueprint: merges reach the next run, with no apply. A tag or commit stays put."
        }
        return plan.follows
            ? "Follows \(draft.blueprint.ref): merges reach the next run, with no apply.\(at)"
            : "Frozen at \(draft.blueprint.ref) until you change it here.\(at)"
    }

    private var runtimesSection: some View {
        Section {
            Picker("Where it runs", selection: kindBinding) {
                Text("On this Mac").tag(Kind.local)
                Text("In the cloud").tag(Kind.cloud)
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .frame(maxWidth: .infinity)

            VStack(alignment: .leading, spacing: 8) {
                Text("Tools")
                FlowLayout(spacing: 6) {
                    ForEach(kind == .local ? Runtime.local : Runtime.cloud, id: \.self) { rt in
                        Chip(title: Runtime.title(rt), isOn: runtimeBinding(rt), help: rt)
                    }
                }
            }
            .padding(.vertical, 2)
            if kind == .cloud {
                TextField("Fabrica server", text: $draft.server, prompt: Text("https://fabrica.example.com"))
                TextField("Other runtimes", text: customRuntimesBinding, prompt: Text("From the blueprint's providers"))
            }
        } header: {
            Text("Where it runs")
        } footer: {
            Footnote {
                if kind == .local {
                    Text("Apply adds Fabrica to each tool you pick. It never writes to your repos.")
                } else if let endpoint = draft.endpoint, !draft.name.isEmpty {
                    Text("Cloud runtimes reach this factory at \(endpoint).")
                } else {
                    Text("Cloud runtimes reach your blueprint through your own Fabrica server, which can run on this Mac.")
                }
            }
        }
    }

    private var targetsSection: some View {
        Section {
            if kind == .local {
                Toggle(isOn: wildcardBinding) {
                    Text("Every repo")
                    Text("Except those another factory names")
                }
            }
            if !draft.coversEveryRepo {
                ForEach($draft.targets) { $target in
                    TargetRow(target: $target) {
                        draft.targets.removeAll { $0.id == target.id }
                    }
                }
                HStack(spacing: 16) {
                    AddButton(title: "Add Repo") { draft.targets.append(.init(repo: "")) }
                    Button("Choose Clones…") { addRepoFromFolder() }
                        .buttonStyle(.borderless)
                        .font(.callout)
                        .help("Pick clones on this Mac, and use their origin remotes")
                }
            }
        } header: {
            Text("Repos")
        } footer: {
            if !draft.coversEveryRepo {
                Footnote { Text("Each repo needs the Fabrica line at the top of its AGENTS.md.") }
            }
        }
    }

    private var routinesSection: some View {
        Section {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text("All routines")
                    Text("Including ones added to the blueprint later")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if let index = draft.routines.firstIndex(where: { $0.name == Factory.wildcard }) {
                    RunsOnMenu(selection: $draft.routines[index].runtimes, runtimes: draft.runtimes, allowsNowhere: false)
                }
                Toggle("All routines", isOn: allRoutinesBinding)
                    .toggleStyle(.switch)
                    .labelsHidden()
                    .controlSize(.small)
            }
            ForEach($draft.routines) { $routine in
                if routine.name != Factory.wildcard {
                    HStack {
                        TextField("Routine", text: $routine.name, prompt: Text("routine-name"))
                            .textFieldStyle(.plain)
                            .labelsHidden()
                        RunsOnMenu(selection: $routine.runtimes, runtimes: draft.runtimes, allowsNowhere: draft.allRoutines != nil)
                        RemoveButton { draft.routines.removeAll { $0.id == routine.id } }
                    }
                }
            }
            AddButton(title: draft.allRoutines != nil ? "Add Exception" : "Add Routine") {
                draft.routines.append(.init(name: ""))
            }
        } header: {
            Text("Routines")
        } footer: {
            Footnote {
                Text(draft.allRoutines != nil
                     ? "A routine named here follows its own setting. Set it to Nowhere to leave it out."
                     : kind == .local ? "Optional. Local routines run as hooks or scheduled tasks in your tools." : "Which of the blueprint's routines to deploy.")
            }
        }
    }

    private var paramsSection: some View {
        Section {
            ParamsEditor(params: $draft.params)
        } header: {
            Text("Params")
        } footer: {
            Footnote { Text("Values for every repo. A repo can override them.") }
        }
    }

    private var optionsSection: some View {
        Section {
            Toggle(isOn: $draft.runsInit) {
                Text("Run the init routine")
                Text("Once, when apply first creates the factory")
            }
        }
    }

    // MARK: Bindings

    private var kindBinding: Binding<Kind> {
        Binding {
            kind
        } set: { new in
            guard new != kind else { return }
            kind = new
            if new == .local {
                draft.runtimes = draft.runtimes.filter { Runtime.local.contains($0) }
                if draft.runtimes.isEmpty { draft.runtimes = ["claude-code-local"] }
                draft.server = ""
            } else {
                draft.runtimes = draft.runtimes.filter { !Runtime.local.contains($0) }
                if draft.runtimes.isEmpty { draft.runtimes = ["claude-code-cloud"] }
                if draft.coversEveryRepo { draft.targets = [] }
                if draft.server.isEmpty, let server = model.home.cloudServers.first { draft.server = server }
            }
            pruneRoutineRuntimes()
        }
    }

    private func runtimeBinding(_ rt: String) -> Binding<Bool> {
        Binding {
            draft.runtimes.contains(rt)
        } set: { on in
            if on {
                // Keep the built-in order, so files don't churn.
                let all = Runtime.local + Runtime.cloud
                draft.runtimes.append(rt)
                draft.runtimes.sort { (all.firstIndex(of: $0) ?? 99) < (all.firstIndex(of: $1) ?? 99) }
            } else {
                draft.runtimes.removeAll { $0 == rt }
            }
            pruneRoutineRuntimes()
        }
    }

    private var customRuntimesBinding: Binding<String> {
        Binding {
            draft.runtimes.filter { !Runtime.cloud.contains($0) && !Runtime.local.contains($0) }.joined(separator: ", ")
        } set: { text in
            let custom = text.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
            draft.runtimes = draft.runtimes.filter { Runtime.cloud.contains($0) } + custom
        }
    }

    private var wildcardBinding: Binding<Bool> {
        Binding {
            draft.coversEveryRepo
        } set: { on in
            draft.targets = on ? [.init(repo: Factory.wildcard)] : []
        }
    }

    private var allRoutinesBinding: Binding<Bool> {
        Binding {
            draft.allRoutines != nil
        } set: { on in
            if on {
                draft.routines.insert(.init(name: Factory.wildcard), at: 0)
            } else {
                draft.routines.removeAll { $0.name == Factory.wildcard }
                // "Nowhere" only means something next to "*".
                for i in draft.routines.indices where draft.routines[i].runtimes == [] {
                    draft.routines[i].runtimes = nil
                }
            }
        }
    }

    /// A routine can only run on the factory's own runtimes.
    private func pruneRoutineRuntimes() {
        for i in draft.routines.indices {
            if let rts = draft.routines[i].runtimes, !rts.isEmpty {
                let kept = rts.filter(draft.runtimes.contains)
                draft.routines[i].runtimes = kept.isEmpty || kept.count == draft.runtimes.count ? nil : kept
            }
        }
    }

    // MARK: Actions

    private func save() {
        var f = draft
        f.targets = f.targets.map { var t = $0; t.repo = canonicalRepo(t.repo); return t }
        f.routines = f.routines.filter { !$0.name.trimmingCharacters(in: .whitespaces).isEmpty }
        f.params = f.params.filter { !$0.key.isEmpty }
        saving = true
        Task {
            defer { saving = false }
            do {
                let file = try await model.home.save(f, replacing: entry?.file)
                draft = f
                original = f
                model.selection = .factory(file)
            } catch {
                saveError = error.localizedDescription
            }
        }
    }

    private func duplicate() {
        var copy = draft
        var n = 2
        while model.home.entries.contains(where: { $0.name == "\(draft.name)-\(n)" }) { n += 1 }
        copy.name = "\(draft.name)-\(n)"
        copy.enabled = false
        Task {
            if let file = try? await model.home.save(copy, replacing: nil) {
                model.selection = .factory(file)
            }
        }
    }

    private func chooseBlueprintFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.message = "Choose a blueprint repo (a folder with a fabrica.yaml, or a repo with one inside)"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        Task {
            // A blueprint in a subfolder of a repo: the repo is the git, the subfolder the path.
            let top = await runCommand(URL(fileURLWithPath: "/usr/bin/git"), ["-C", url.path, "rev-parse", "--show-toplevel"])
            let root = top.ok ? top.stdout.trimmingCharacters(in: .whitespacesAndNewlines) : url.path
            draft.blueprint.git = root
            let sub = url.path.hasPrefix(root + "/") ? String(url.path.dropFirst(root.count + 1)) : ""
            draft.blueprint.path = sub
            let branch = await runCommand(URL(fileURLWithPath: "/usr/bin/git"), ["-C", root, "branch", "--show-current"])
            let name = branch.stdout.trimmingCharacters(in: .whitespacesAndNewlines)
            if branch.ok, !name.isEmpty { draft.blueprint.ref = name }
        }
    }

    private func addRepoFromFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = true
        panel.message = "Choose one or more clones of your product repos"
        guard panel.runModal() == .OK else { return }
        let urls = panel.urls
        Task {
            for url in urls {
                let remote = await runCommand(URL(fileURLWithPath: "/usr/bin/git"), ["-C", url.path, "remote", "get-url", "origin"])
                let repo = canonicalRepo(remote.stdout.trimmingCharacters(in: .whitespacesAndNewlines))
                if remote.ok, !repo.isEmpty, !draft.targets.contains(where: { $0.repo == repo }) {
                    draft.targets.append(.init(repo: repo))
                }
            }
        }
    }

    private func unreadable(_ entry: HomeRepo.Entry) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Label("This factory file can't be shown in the editor", systemImage: "exclamationmark.triangle")
                .font(.headline)
            Text(entry.problem ?? "")
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
            LogView(text: entry.raw)
            HStack {
                Button("Show in Finder") {
                    NSWorkspace.shared.activateFileViewerSelecting([model.home.factoriesURL.appending(path: entry.file)])
                }
                Button("Delete…", role: .destructive) { confirmingDelete = true }
            }
        }
        .padding(20)
        .confirmationDialog("Delete \(entry.name)?", isPresented: $confirmingDelete) {
            Button("Delete", role: .destructive) {
                Task { await model.home.delete(entry); model.selection = nil }
            }
        }
    }
}

/// Repo names the way Fabrica writes them: host/owner/repo, in lowercase.
func canonicalRepo(_ s: String) -> String {
    var r = s.trimmingCharacters(in: .whitespacesAndNewlines)
    if r == Factory.wildcard { return r }
    for prefix in ["https://", "http://", "ssh://", "git://"] where r.hasPrefix(prefix) {
        r = String(r.dropFirst(prefix.count))
    }
    if let at = r.firstIndex(of: "@"), r[..<at].allSatisfy({ $0.isLetter || $0.isNumber || $0 == "-" || $0 == "_" }) {
        r = String(r[r.index(after: at)...])
    }
    // git@github.com:owner/repo → github.com/owner/repo
    if let colon = r.firstIndex(of: ":"), !r[..<colon].contains("/") {
        let after = r[r.index(after: colon)...]
        r = String(r[..<colon]) + "/" + (after.first?.isNumber == true ? String(after.drop { $0.isNumber }.drop { $0 == "/" }) : String(after))
    }
    if r.hasSuffix(".git") { r = String(r.dropLast(4)) }
    return r.trimmingCharacters(in: CharacterSet(charactersIn: "/")).lowercased()
}

private struct TargetRow: View {
    @Binding var target: Factory.Target
    var onRemove: () -> Void
    @State private var showsParams = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                TextField("Repo", text: $target.repo, prompt: Text("github.com/owner/repo"))
                    .textFieldStyle(.plain)
                    .labelsHidden()
                Button {
                    withAnimation(.snappy(duration: 0.2)) { showsParams.toggle() }
                } label: {
                    HStack(spacing: 3) {
                        Image(systemName: "slider.horizontal.3")
                        if !target.params.isEmpty { Text("\(target.params.count)").monospacedDigit() }
                    }
                    .font(.callout)
                    .foregroundStyle(target.params.isEmpty && !showsParams ? AnyShapeStyle(.tertiary) : AnyShapeStyle(.tint))
                }
                .buttonStyle(.borderless)
                .help("Params for this repo only")
                RemoveButton(action: onRemove)
            }
            if showsParams || !target.params.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    ParamsEditor(params: $target.params)
                }
                .padding(.leading, 14)
                .overlay(alignment: .leading) {
                    Rectangle().fill(.separator).frame(width: 1)
                }
            }
        }
    }
}

private struct ParamsEditor: View {
    @Binding var params: [Factory.Param]

    var body: some View {
        ForEach($params) { $param in
            HStack(spacing: 8) {
                TextField("Name", text: $param.key, prompt: Text("name"))
                    .textFieldStyle(.plain)
                    .labelsHidden()
                    .font(.body.monospaced())
                    .frame(maxWidth: 170, alignment: .leading)
                TextField("Value", text: $param.value, prompt: Text("value"))
                    .textFieldStyle(.plain)
                    .labelsHidden()
                RemoveButton { params.removeAll { $0.id == param.id } }
            }
        }
        AddButton(title: "Add Param") { params.append(.init(key: "", value: "")) }
    }
}

/// Picks which of the factory's runtimes a routine runs on.
private struct RunsOnMenu: View {
    @Binding var selection: [String]?
    var runtimes: [String]
    var allowsNowhere: Bool

    var body: some View {
        Menu(label) {
            Button {
                selection = nil
            } label: {
                if selection == nil { Image(systemName: "checkmark") }
                Text("Every runtime")
            }
            Divider()
            ForEach(runtimes, id: \.self) { rt in
                Toggle(Runtime.title(rt), isOn: Binding {
                    selection?.contains(rt) ?? false
                } set: { on in
                    var current = selection ?? []
                    if on { current.append(rt) } else { current.removeAll { $0 == rt } }
                    current = runtimes.filter(current.contains)
                    selection = current.isEmpty ? (allowsNowhere ? [] : nil) : (current.count == runtimes.count ? nil : current)
                })
            }
            if allowsNowhere {
                Divider()
                Button {
                    selection = []
                } label: {
                    if selection == [] { Image(systemName: "checkmark") }
                    Text("Nowhere")
                }
            }
        }
        .menuStyle(.borderlessButton)
        .fixedSize()
        .foregroundStyle(.secondary)
    }

    private var label: String {
        switch selection {
        case nil: "Every runtime"
        case .some(let list) where list.isEmpty: "Nowhere"
        case .some(let list): list.map(Runtime.title).joined(separator: ", ")
        }
    }
}
