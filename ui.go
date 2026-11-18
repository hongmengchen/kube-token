package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type viewState int

const (
	viewList viewState = iota
	viewInput
	viewConfirm
	viewWorking
	viewResult
	viewPass
	viewConfig
	viewMessage
	viewKubectl
	viewKcInstall
)

var (
	cAccent = lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#A78BFA"}
	cDim    = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#7C8794"}
	cText   = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#E5E7EB"}
	cGood   = lipgloss.AdaptiveColor{Light: "#047857", Dark: "#34D399"}
	cBad    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	cWarn   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	cLine   = lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#333A45"}

	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	accentStyle = lipgloss.NewStyle().Foreground(cAccent)
	dimStyle    = lipgloss.NewStyle().Foreground(cDim)
	textStyle   = lipgloss.NewStyle().Foreground(cText)
	goodStyle   = lipgloss.NewStyle().Foreground(cGood).Bold(true)
	badStyle    = lipgloss.NewStyle().Foreground(cBad)
	warnStyle   = lipgloss.NewStyle().Foreground(cWarn)
	labelStyle  = lipgloss.NewStyle().Foreground(cDim).Width(9)
	keyStyle    = lipgloss.NewStyle().Foreground(cAccent).Bold(true)

	cardHi  = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(cAccent).PaddingLeft(1)
	cardDim = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(cLine).PaddingLeft(1)
)

type model struct {
	entries  []Entry
	scanErrs []error
	cursor   int
	state    viewState

	width, height int

	input    textinput.Model
	selected Entry
	backup   string
	result   string
	resultOK bool
	wrote    bool
	note     string

	// Vault
	cfg       *vaultConfig
	pass      textinput.Model
	fields    []textinput.Model
	focus     int
	source    string // token 来源：Vault / 手动
	workText  string
	message   string
	messageOK bool

	// 写入方式：kubectl / 内置
	kubectlPath string
	method      string

	// 一键安装官方 kubectl 的动画（和 upgrade 一致）
	kcBar       progress.Model
	kcCh        chan tea.Msg
	kcGen       int
	kcDir       string
	kcVersion   string
	kcPhase     kcPhase
	kcDone      int64
	kcTotal     int64
	kcSpeed     float64
	kcLastBytes int64
	kcLastTime  time.Time

	spin spinner.Model
}

type applyDoneMsg struct {
	backup  string
	verify  string
	applied bool
	err     error
}

type vaultDoneMsg struct {
	token string
	err   error
}

type kubectlInstalledMsg struct {
	path string
	err  error
}

type kcPhase int

const (
	kcDownloading kcPhase = iota
	kcInstalling
)

// 带 gen：每次重新安装换一代，避免上一次残留的通道消息串进新的动画
type kubectlProgressMsg struct {
	gen         int
	done, total int64
}

type kubectlProgressClosedMsg struct{ gen int }

type kubectlVersionMsg struct {
	gen     int
	version string
}

func newModel(entries []Entry, scanErrs []error) model {
	ti := textinput.New()
	ti.Placeholder = "粘贴 token"
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	ti.Prompt = accentStyle.Render("❯ ")
	ti.CharLimit = 4096

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(cAccent)

	pw := textinput.New()
	pw.Placeholder = "Vault 密码"
	pw.EchoMode = textinput.EchoPassword
	pw.EchoCharacter = '•'
	pw.Prompt = accentStyle.Render("❯ ")
	pw.CharLimit = 1024

	m := model{entries: entries, scanErrs: scanErrs, input: ti, spin: sp, pass: pw}
	if cfg, err := loadVaultConfig(); err != nil {
		m.note = "读取 Vault 配置失败: " + err.Error()
	} else {
		m.cfg = cfg
	}
	return m
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeKcBar()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case kubectlVersionMsg:
		if msg.gen != m.kcGen {
			return m, nil
		}
		m.kcVersion = msg.version
		// 收到一条就要继续等下一帧，否则进度消息全堵在通道里
		return m, m.kcWaitProgress()

	case kubectlProgressMsg:
		m.kcDone, m.kcTotal = msg.done, msg.total
		now := time.Now()
		if d := now.Sub(m.kcLastTime).Seconds(); d >= 0.2 {
			m.kcSpeed = float64(m.kcDone-m.kcLastBytes) / d
			m.kcLastBytes, m.kcLastTime = m.kcDone, now
		}
		next := m.kcWaitProgress()
		if m.kcTotal > 0 {
			return m, tea.Batch(m.kcBar.SetPercent(float64(m.kcDone)/float64(m.kcTotal)), next)
		}
		return m, next

	case kubectlProgressClosedMsg:
		if msg.gen == m.kcGen && m.state == viewKcInstall {
			m.kcPhase = kcInstalling
		}
		return m, nil

	case progress.FrameMsg:
		if m.state != viewKcInstall {
			return m, nil
		}
		barModel, cmd := m.kcBar.Update(msg)
		if b, ok := barModel.(progress.Model); ok {
			m.kcBar = b
		}
		return m, cmd

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m.handleKey(msg)

	case kubectlInstalledMsg:
		if msg.err != nil {
			m.state = viewKubectl
			m.note = "安装失败: " + msg.err.Error()
			return m, nil
		}
		m.kubectlPath = msg.path
		m.method = "官方 kubectl"
		m.state = viewWorking
		m.workText = "正在写入并验证 token…"
		tok := strings.TrimSpace(m.input.Value())
		return m, tea.Batch(m.spin.Tick, applyCmd(m.selected, tok, msg.path))

	case vaultDoneMsg:
		if msg.err != nil {
			m.state = viewMessage
			m.message = msg.err.Error()
			m.messageOK = false
			return m, nil
		}
		m.input.SetValue(msg.token)
		m.input.CursorEnd()
		m.source = "Vault"
		m.state = viewConfirm
		return m, nil

	case applyDoneMsg:
		m.state = viewResult
		m.backup = msg.backup
		m.wrote = msg.applied
		m.resultOK = msg.err == nil
		switch {
		case msg.err == nil:
			m.result = msg.verify
		case msg.applied:
			m.result = "token 已写入，但验证失败：" + msg.err.Error()
		default:
			m.result = msg.err.Error()
		}
		return m, nil
	}

	// 输入框还需要接收光标闪烁之类的消息
	switch m.state {
	case viewInput, viewConfirm:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	case viewPass:
		var cmd tea.Cmd
		m.pass, cmd = m.pass.Update(msg)
		return m, cmd
	case viewConfig:
		if len(m.fields) > 0 && m.focus < len(m.fields) {
			var cmd tea.Cmd
			m.fields[m.focus], cmd = m.fields[m.focus].Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.state {
	case viewList:
		switch k.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.entries) > 0 {
				m.selected = m.entries[m.cursor]
				m.input.SetValue("")
				m.input.Focus()
				m.note = ""
				m.state = viewInput
				return m, textinput.Blink
			}
		case "r":
			m.entries, m.scanErrs = scanEntries()
			m.clampCursor()
		case "v":
			return m.startVaultFlow()
		case "c":
			m.openConfigForm()
			m.state = viewConfig
			return m, textinput.Blink
		case "q", "esc":
			return m, tea.Quit
		}

	case viewInput:
		switch k.String() {
		case "esc":
			m.input.Blur()
			m.state = viewList
			return m, nil
		case "ctrl+y":
			if s, err := readClipboard(); err != nil {
				m.note = err.Error()
			} else if s == "" {
				m.note = "剪贴板是空的"
			} else {
				m.input.SetValue(s)
				m.input.CursorEnd()
				m.note = fmt.Sprintf("已从剪贴板读取 %d 个字符", len(s))
			}
			return m, nil
		case "enter":
			tok := strings.TrimSpace(m.input.Value())
			if tok == "" {
				m.note = "token 不能为空"
				return m, nil
			}
			if strings.ContainsAny(tok, " \t\r\n") {
				m.note = "token 里含有空白字符，请检查粘贴内容"
				return m, nil
			}
			m.state = viewConfirm
			m.input.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return m, cmd

	case viewConfirm:
		switch k.String() {
		case "esc":
			// 从 Vault 来的 token 回退时直接回列表，避免退回一个空输入框
			if m.source == "Vault" {
				m.source = ""
				m.state = viewList
				return m, nil
			}
			m.state = viewInput
			m.input.Focus()
			return m, textinput.Blink
		case "enter":
			tok := strings.TrimSpace(m.input.Value())
			if kp, ok := findKubectl(); ok {
				m.kubectlPath = kp
				m.method = "官方 kubectl"
				m.state = viewWorking
				m.workText = "正在写入并验证 token…"
				return m, tea.Batch(m.spin.Tick, applyCmd(m.selected, tok, kp))
			}
			// 没有 kubectl：先问要不要装官方版，再决定用哪种方式写
			m.state = viewKubectl
			m.note = ""
			return m, nil
		}

	case viewPass:
		switch k.String() {
		case "esc":
			m.pass.Blur()
			m.source = ""
			m.state = viewList
			return m, nil
		case "enter":
			pw := m.pass.Value()
			if strings.TrimSpace(pw) == "" {
				m.note = "密码不能为空"
				return m, nil
			}
			m.pass.Blur()
			m.state = viewWorking
			m.workText = "正在登录 Vault 并获取 token…"
			cfg := m.cfg
			return m, tea.Batch(m.spin.Tick, func() tea.Msg {
				tok, err := vaultFetchToken(cfg, pw)
				return vaultDoneMsg{token: tok, err: err}
			})
		}
		var cmd tea.Cmd
		m.pass, cmd = m.pass.Update(k)
		return m, cmd

	case viewConfig:
		switch k.String() {
		case "esc":
			m.state = viewList
			m.note = "已取消"
			return m, nil
		case "up", "shift+tab":
			m.moveFocus(-1)
			return m, nil
		case "down", "tab":
			m.moveFocus(1)
			return m, nil
		case "ctrl+s":
			m.saveConfigFromForm()
			return m, nil
		}
		if len(m.fields) > 0 {
			var cmd tea.Cmd
			m.fields[m.focus], cmd = m.fields[m.focus].Update(k)
			return m, cmd
		}
		return m, nil

	case viewKubectl:
		switch k.String() {
		case "esc":
			m.state = viewConfirm
			return m, nil
		case "enter":
			return m.startKubectlInstall()
		case "n":
			m.kubectlPath = ""
			m.method = "内置（未装 kubectl）"
			m.state = viewWorking
			m.workText = "正在写入并验证 token…"
			tok := strings.TrimSpace(m.input.Value())
			return m, tea.Batch(m.spin.Tick, applyCmd(m.selected, tok, ""))
		}
		return m, nil

	case viewWorking:
		// 处理过程中忽略按键，等结果

	case viewMessage:
		m.state = viewList
		return m, nil

	case viewResult:
		m.state = viewList
		m.source = ""
		m.entries, m.scanErrs = scanEntries()
		m.clampCursor()
		return m, nil
	}
	return m, nil
}

func (m *model) showMessage(text string, ok bool) {
	m.state = viewMessage
	m.message = text
	m.messageOK = ok
}

func (m *model) startVaultFlow() (tea.Model, tea.Cmd) {
	if len(m.entries) == 0 {
		m.showMessage("没有可用的 kubeconfig 条目", false)
		return m, nil
	}
	if m.cfg == nil {
		m.openConfigForm()
		m.state = viewConfig
		m.note = ""
		return m, textinput.Blink
	}

	entry := m.entries[m.cursor]
	if m.cfg.Target.User != "" || m.cfg.Target.File != "" {
		found, err := pickEntry(m.entries, m.cfg.Target.User, m.cfg.Target.File)
		if err != nil {
			m.showMessage(err.Error(), false)
			return m, nil
		}
		entry = found
	}

	m.selected = entry
	m.source = "Vault"
	m.pass.SetValue("")
	m.pass.Focus()
	m.note = ""
	m.state = viewPass
	return m, textinput.Blink
}

var configLabels = []string{
	"Vault 地址", "认证方式", "认证路径", "用户名",
	"密钥路径", "请求参数", "目标文件", "目标 user",
}

func (m *model) openConfigForm() {
	cfg := &vaultConfig{}
	if m.cfg != nil {
		*cfg = *m.cfg
	} else {
		cfg.Addr = os.Getenv("VAULT_ADDR")
		cfg.Auth.Method = "ldap"
		cfg.Auth.Path = "ldap"
		cfg.Auth.Username = os.Getenv("USER")
		if cfg.Auth.Username == "" {
			cfg.Auth.Username = os.Getenv("USERNAME")
		}
	}
	normalizeVaultConfig(cfg)

	values := []string{
		cfg.Addr, cfg.Auth.Method, cfg.Auth.Path, cfg.Auth.Username,
		cfg.Secret.Path, encodeParams(cfg.Secret.Params), cfg.Target.File, cfg.Target.User,
	}
	placeholders := []string{
		"https://vault.example.com", "ldap", "ldap", "你的用户名",
		"<mount>/creds/<role>", "<key>=<value>", "(留空=当前扫描到的文件)", "(留空=当前 context 的 user)",
	}

	m.fields = make([]textinput.Model, len(values))
	for i, v := range values {
		ti := textinput.New()
		ti.Prompt = ""
		ti.SetValue(v)
		ti.Placeholder = placeholders[i]
		ti.CharLimit = 1024
		m.fields[i] = ti
	}
	m.focus = 0
	m.fields[0].Focus()
	m.note = ""
}

func (m *model) moveFocus(delta int) {
	if len(m.fields) == 0 {
		return
	}
	m.fields[m.focus].Blur()
	m.focus = (m.focus + delta + len(m.fields)) % len(m.fields)
	m.fields[m.focus].Focus()
}

func (m *model) saveConfigFromForm() {
	if len(m.fields) < len(configLabels) {
		return
	}
	get := func(i int) string { return strings.TrimSpace(m.fields[i].Value()) }

	cfg := &vaultConfig{
		Addr: get(0),
		Auth: vaultAuth{Method: get(1), Path: get(2), Username: get(3)},
		Secret: vaultSecret{
			Path:   get(4),
			Params: parseParams(get(5)),
		},
		Target: vaultTarget{File: expandHome(get(6)), User: get(7)},
	}
	if m.cfg != nil {
		cfg.Namespace = m.cfg.Namespace
		cfg.TLS = m.cfg.TLS
	}
	normalizeVaultConfig(cfg)

	if err := cfg.validate(); err != nil {
		m.note = err.Error()
		return
	}
	path, err := saveVaultConfig(cfg)
	if err != nil {
		m.note = "保存失败: " + err.Error()
		return
	}
	m.cfg = cfg
	m.state = viewList
	m.note = "已保存到 " + shortenHome(path) + "，按 v 获取 token"
}

// encodeParams / parseParams：把 map 在表单里表示成 k=v,k2=v2
func encodeParams(params map[string]string) string {
	var parts []string
	for k, v := range params {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func parseParams(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	}) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			out[part] = ""
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

func applyCmd(e Entry, token, kubectlPath string) tea.Cmd {
	return func() tea.Msg {
		backup, err := applyTokenWith(e, token, kubectlPath)
		if err != nil {
			return applyDoneMsg{backup: backup, applied: backup != "", err: err}
		}
		verify, verr := verifyClusterWith(e.File, kubectlPath)
		if verr != nil {
			return applyDoneMsg{backup: backup, applied: true, err: verr}
		}
		return applyDoneMsg{backup: backup, applied: true, verify: verify}
	}
}

// ---------- 一键安装官方 kubectl（动画和 upgrade 一致） ----------

func (m *model) startKubectlInstall() (tea.Model, tea.Cmd) {
	m.kcBar = progress.New(progress.WithGradient("#6D28D9", "#A78BFA"), progress.WithoutPercentage())
	m.resizeKcBar()
	m.state = viewKcInstall
	m.kcPhase = kcDownloading
	m.kcVersion = ""
	m.kcDir = ""
	if d, err := kubectlInstallDir(); err == nil {
		m.kcDir = d
	}
	m.kcDone, m.kcTotal, m.kcSpeed, m.kcLastBytes = 0, 0, 0, 0
	m.kcLastTime = time.Now()
	m.kcGen++
	m.kcCh = make(chan tea.Msg, 128)
	return m, tea.Batch(m.spin.Tick, m.kcInstallCmd(), m.kcWaitProgress())
}

func (m *model) resizeKcBar() {
	w := m.width - 20
	if w > 44 {
		w = 44
	}
	if w < 12 {
		w = 12
	}
	m.kcBar.Width = w
}

func (m *model) kcInstallCmd() tea.Cmd {
	ch, gen := m.kcCh, m.kcGen
	return func() tea.Msg {
		defer close(ch)
		send := func(msg tea.Msg) {
			select {
			case ch <- msg:
			default: // 界面来不及消费就丢掉这帧，别把下载卡住
			}
		}
		path, err := installKubectlWith("", func(v string) {
			send(kubectlVersionMsg{gen: gen, version: v})
		}, func(done, total int64) {
			send(kubectlProgressMsg{gen: gen, done: done, total: total})
		})
		return kubectlInstalledMsg{path: path, err: err}
	}
}

func (m *model) kcWaitProgress() tea.Cmd {
	ch, gen := m.kcCh, m.kcGen
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return kubectlProgressClosedMsg{gen: gen}
		}
		return msg
	}
}

func (m *model) clampCursor() {
	if m.cursor >= len(m.entries) {
		m.cursor = len(m.entries) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// ---------- 渲染 ----------

func (m model) View() string {
	var body string
	switch m.state {
	case viewList:
		body = m.viewList()
	case viewInput:
		body = m.viewInput()
	case viewConfirm:
		body = m.viewConfirm()
	case viewWorking:
		body = m.viewWorking()
	case viewResult:
		body = m.viewResult()
	case viewPass:
		body = m.viewPass()
	case viewConfig:
		body = m.viewConfig()
	case viewMessage:
		body = m.viewMessage()
	case viewKubectl:
		body = m.viewKubectl()
	case viewKcInstall:
		body = m.viewKcInstall()
	}
	return "\n" + body + "\n"
}

func (m model) viewList() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("kube-token") + dimStyle.Render("  选择要更新的 user"))
	b.WriteString("\n")
	if len(m.entries) > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %d 个 user · %d 个文件", len(m.entries), countFiles(m.entries))))
	} else {
		b.WriteString(dimStyle.Render("  没有发现可用的 kubeconfig"))
	}
	b.WriteString("\n\n")

	for i, e := range m.entries {
		b.WriteString(m.renderCard(e, i == m.cursor))
		b.WriteString("\n")
	}
	for _, err := range m.scanErrs {
		b.WriteString(warnStyle.Render("  ! " + err.Error()))
		b.WriteString("\n")
	}

	if m.note != "" {
		b.WriteString("\n  " + accentStyle.Render(m.note) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(hints("↑↓", "选择", "enter", "手动粘贴", "v", "从 Vault 获取", "c", "配置", "q", "退出"))
	return b.String()
}

func (m model) renderCard(e Entry, selected bool) string {
	w := m.contentWidth()

	expLabel, _, expired := expiryLabel(e)
	exp := dimStyle.Render(expLabel)
	if e.HasExp {
		if expired {
			exp = badStyle.Render(expLabel)
		} else {
			exp = goodStyle.Render(expLabel)
		}
	}

	// 不用 ◆ ● 这类 East-Asian ambiguous 宽度字符：lipgloss 按 1 格算、终端按 2 格渲染会折行
	tag := ""
	if e.IsCurrent {
		tag = accentStyle.Render("当前 context")
	}
	fileAvail := w
	if tag != "" {
		fileAvail = w - lipgloss.Width(tag) - 1
	}
	fileShown := textStyle.Render(truncate(shortenHome(e.File), fileAvail))
	gap := w - lipgloss.Width(fileShown) - lipgloss.Width(tag)
	if gap < 1 {
		gap = 1
	}
	fileLine := fileShown + strings.Repeat(" ", gap) + tag

	userLine := labelStyle.Render("user") + textStyle.Render(truncate(e.User, w-10))

	tokenLine := labelStyle.Render("token")
	if e.HasToken() {
		tokenLine += dimStyle.Render(maskToken(e.Token)) + dimStyle.Render("  ·  ") + exp
	} else {
		tokenLine += warnStyle.Render("(空)")
	}
	if e.HasExec {
		tokenLine += "  " + warnStyle.Render("exec 自动续期")
	}

	body := fileLine + "\n" + userLine + "\n" + tokenLine
	if selected {
		return cardHi.Render(body)
	}
	return cardDim.Render(body)
}

func (m model) summary(e Entry) string {
	expLabel, _, expired := expiryLabel(e)
	exp := dimStyle.Render(expLabel)
	if e.HasExp {
		if expired {
			exp = badStyle.Render(expLabel)
		} else {
			exp = goodStyle.Render(expLabel)
		}
	}
	lines := []string{
		labelStyle.Render("文件") + textStyle.Render(shortenHome(e.File)),
		labelStyle.Render("user") + textStyle.Render(truncate(e.User, m.contentWidth()-10)),
		labelStyle.Render("当前") + dimStyle.Render(maskToken(e.Token)) + dimStyle.Render("  ·  ") + exp,
	}
	return "  " + strings.Join(lines, "\n  ")
}

func (m model) viewInput() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("kube-token") + dimStyle.Render("  粘贴新 token"))
	b.WriteString("\n\n")
	b.WriteString(m.summary(m.selected))
	b.WriteString("\n\n")
	b.WriteString("  " + dimStyle.Render("新 token") + "\n")
	b.WriteString("  " + m.input.View() + "\n\n")
	if m.note != "" {
		b.WriteString("  " + accentStyle.Render(m.note) + "\n\n")
	} else {
		b.WriteString("  " + dimStyle.Render("粘贴后回车；也可以按 ctrl+y 从剪贴板读取") + "\n\n")
	}
	b.WriteString(hints("enter", "下一步", "ctrl+y", "读剪贴板", "esc", "返回"))
	return b.String()
}

func (m model) viewConfirm() string {
	e := m.selected
	tok := strings.TrimSpace(m.input.Value())

	var b strings.Builder
	b.WriteString(titleStyle.Render("kube-token") + dimStyle.Render("  确认替换"))
	b.WriteString("\n\n")
	b.WriteString("  " + labelStyle.Render("文件") + textStyle.Render(shortenHome(e.File)) + "\n")
	b.WriteString("  " + labelStyle.Render("user") + textStyle.Render(truncate(e.User, m.contentWidth()-10)) + "\n")
	b.WriteString("  " + labelStyle.Render("旧") + dimStyle.Render(maskToken(e.Token)) + "\n")
	b.WriteString("  " + labelStyle.Render("新") + accentStyle.Render(maskToken(tok)) + "\n")
	if m.source != "" {
		b.WriteString("  " + labelStyle.Render("来源") + dimStyle.Render(m.source) + "\n")
	}
	b.WriteString("\n")
	b.WriteString("  " + dimStyle.Render("会先备份成 "+shortenHome(e.File)+".bak.<时间戳>") + "\n")

	if e.HasExec {
		b.WriteString("\n  " + warnStyle.Render("注意：该 user 使用 exec 插件自动续期，写死 token 可能与它冲突") + "\n")
	}
	b.WriteString("\n")
	b.WriteString(hints("enter", "应用", "esc", "返回"))
	return b.String()
}

func (m model) viewWorking() string {
	text := m.workText
	if text == "" {
		text = "正在处理…"
	}
	return titleStyle.Render("kube-token") + dimStyle.Render("  处理中") + "\n\n" +
		"  " + m.spin.View() + "  " + dimStyle.Render(text)
}

func (m model) viewPass() string {
	user := ""
	if m.cfg != nil {
		user = m.cfg.Auth.Username
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("kube-token") + dimStyle.Render("  从 Vault 获取 token"))
	b.WriteString("\n\n")
	b.WriteString(m.summary(m.selected))
	b.WriteString("\n\n")
	if m.cfg != nil {
		b.WriteString("  " + labelStyle.Render("Vault") + dimStyle.Render(m.cfg.Addr) + "\n")
		b.WriteString("  " + labelStyle.Render("登录") + textStyle.Render(m.cfg.Auth.Method+" / "+user) + "\n")
		b.WriteString("  " + labelStyle.Render("密钥") + dimStyle.Render(m.cfg.Secret.Path) + "\n\n")
	}
	b.WriteString("  " + dimStyle.Render("密码") + "\n")
	b.WriteString("  " + m.pass.View() + "\n\n")
	if m.note != "" {
		b.WriteString("  " + warnStyle.Render(m.note) + "\n\n")
	} else {
		b.WriteString("  " + dimStyle.Render("密码只用于这次登录，不会保存") + "\n\n")
	}
	b.WriteString(hints("enter", "登录并获取", "esc", "返回"))
	return b.String()
}

func (m model) viewConfig() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("kube-token") + dimStyle.Render("  配置（只保存在本机，不会上传）"))
	b.WriteString("\n\n")

	for i, f := range m.fields {
		label := fmt.Sprintf("%-10s", configLabels[i])
		style := dimStyle.Render(label)
		value := f.View()
		if i == m.focus {
			style = accentStyle.Render(label)
			value = accentStyle.Render(f.View())
		}
		b.WriteString("  " + style + " " + value + "\n")
	}

	b.WriteString("\n  " + dimStyle.Render("请求参数写成 k=v，多个用逗号分隔；留空的项使用默认值") + "\n")
	if m.note != "" {
		b.WriteString("  " + warnStyle.Render(m.note) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(hints("↑↓", "切换", "enter", "编辑", "ctrl+s", "保存", "esc", "取消"))
	return b.String()
}

func (m model) viewKubectl() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("kube-token") + "  " + warnStyle.Render("没有找到 kubectl"))
	b.WriteString("\n\n")
	b.WriteString("  " + textStyle.Render("用官方 kubectl 写入最稳妥，行为和命令行完全一致；") + "\n")
	b.WriteString("  " + textStyle.Render("也可以不装，由本程序直接改 YAML（照样先备份，注释和其它字段保留）。") + "\n\n")
	if m.note != "" {
		b.WriteString("  " + badStyle.Render(m.note) + "\n\n")
	}
	b.WriteString(hints("enter", "一键安装官方 kubectl", "n", "不安装，用内置方式", "esc", "返回"))
	return b.String()
}

// viewKcInstall 和 `kube-token upgrade` 用同一套动画：spinner + 渐变进度条 + 大小/速度
func (m model) viewKcInstall() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("kube-token") + dimStyle.Render("  安装官方 kubectl"))
	b.WriteString("\n\n")

	switch m.kcPhase {
	case kcInstalling:
		b.WriteString("  " + m.spin.View() + " " + textStyle.Render("正在校验并安装…"))
		b.WriteString("\n\n  " + m.kcBar.View())
		b.WriteString("\n\n  " + dimStyle.Render(m.kcProgressText()))
	default:
		title := "正在下载官方 kubectl"
		if m.kcVersion != "" {
			title += " " + m.kcVersion
		}
		b.WriteString("  " + m.spin.View() + " " + textStyle.Render(title))
		if m.kcTotal > 0 {
			b.WriteString("\n\n  " + m.kcBar.View())
			b.WriteString("\n\n  " + dimStyle.Render(m.kcProgressText()))
		} else {
			b.WriteString("\n\n  " + dimStyle.Render("正在连接 dl.k8s.io…"))
		}
	}

	if m.kcDir != "" {
		b.WriteString("\n\n  " + dimStyle.Render("装到 "+shortenHome(m.kcDir)))
	}
	return b.String()
}

func (m model) kcProgressText() string {
	mb := func(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }
	speed := ""
	if m.kcSpeed > 0 {
		speed = fmt.Sprintf("  %s/s", mb(int64(m.kcSpeed)))
	}
	if m.kcTotal > 0 {
		pct := 100 * float64(m.kcDone) / float64(m.kcTotal)
		return fmt.Sprintf("%5.1f%%  %s / %s%s", pct, mb(m.kcDone), mb(m.kcTotal), speed)
	}
	return mb(m.kcDone) + speed
}

func (m model) viewMessage() string {
	head := badStyle.Render("✗ 操作失败")
	if m.messageOK {
		head = goodStyle.Render("✓ 完成")
	}
	return titleStyle.Render("kube-token") + "  " + head + "\n\n" +
		"  " + textStyle.Render(m.message) + "\n\n" +
		hints("任意键", "返回")
}

func (m model) viewResult() string {
	e := m.selected

	var head string
	switch {
	case m.resultOK:
		head = goodStyle.Render("✓ 已更新")
	case m.wrote:
		head = warnStyle.Render("! 已写入，但验证未通过")
	default:
		head = badStyle.Render("✗ 替换失败")
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("kube-token") + "  " + head)
	b.WriteString("\n\n")
	b.WriteString("  " + labelStyle.Render("文件") + textStyle.Render(shortenHome(e.File)) + "\n")
	b.WriteString("  " + labelStyle.Render("user") + textStyle.Render(truncate(e.User, m.contentWidth()-10)) + "\n")
	if m.method != "" {
		b.WriteString("  " + labelStyle.Render("方式") + dimStyle.Render(m.method) + "\n")
	}
	if m.backup != "" {
		b.WriteString("  " + labelStyle.Render("备份") + dimStyle.Render(shortenHome(m.backup)) + "\n")
	}
	if m.result != "" {
		label := "验证"
		style := goodStyle
		switch {
		case !m.resultOK && m.wrote:
			label, style = "问题", warnStyle
		case !m.resultOK:
			label, style = "错误", badStyle
		}
		b.WriteString("  " + labelStyle.Render(label) + style.Render(m.result) + "\n")
	}
	if !m.resultOK && m.wrote && m.backup != "" {
		b.WriteString("\n  " + dimStyle.Render("回滚: cp "+shortenHome(m.backup)+" "+shortenHome(e.File)) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(hints("任意键", "返回列表"))
	return b.String()
}

func (m model) contentWidth() int {
	w := m.width - 4
	if w < 40 {
		w = 40
	}
	if w > 110 {
		w = 110
	}
	return w
}

func hints(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, keyStyle.Render(pairs[i])+dimStyle.Render(" "+pairs[i+1]))
	}
	return "  " + strings.Join(parts, dimStyle.Render("  ·  "))
}

func countFiles(entries []Entry) int {
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.File] = true
	}
	return len(seen)
}

func shortenHome(p string) string {
	home, err := homeDir()
	if err != nil || home == "" {
		return p
	}
	if strings.HasPrefix(p, home+"/") || strings.HasPrefix(p, home+"\\") {
		return "~" + p[len(home):]
	}
	return p
}

func truncate(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > w-1 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}
