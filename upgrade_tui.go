package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

type upPhase int

const (
	upChecking upPhase = iota
	upDownloading
	upInstalling
	upLatest
	upDone
	upFailed
)

type upgradeModel struct {
	spin spinner.Model
	bar  progress.Model
	ch   chan upgradeProgressMsg

	phase  upPhase
	force  bool
	exe    string
	rel    *releaseInfo
	url    string
	tmp    string
	latest string
	err    error

	done, total int64
	speed       float64
	lastBytes   int64
	lastTime    time.Time
	width       int
}

type upgradeCheckMsg struct {
	rel *releaseInfo
	err error
}

type upgradeProgressMsg struct{ done, total int64 }

type upgradeProgressClosedMsg struct{}

type upgradeDownloadedMsg struct {
	tmp string
	err error
}

type upgradeInstalledMsg struct{ err error }

type upgradeQuitMsg struct{}

type upgradeInstallStartMsg struct{}

func runUpgradeTUI(force bool) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "找不到当前可执行文件: %v\n", err)
		return 1
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = accentStyle

	m := &upgradeModel{
		spin:     sp,
		bar:      progress.New(progress.WithGradient("#6D28D9", "#A78BFA"), progress.WithoutPercentage()),
		phase:    upChecking,
		force:    force,
		exe:      exe,
		lastTime: time.Now(),
	}
	m.bar.Width = 40

	p := tea.NewProgram(m)
	final, runErr := p.Run()
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "升级界面出错: %v\n", runErr)
		return 1
	}

	fm, ok := final.(*upgradeModel)
	if !ok {
		return 1
	}
	if fm.err != nil {
		fmt.Fprintf(os.Stderr, "\n更新失败: %v\n", fm.err)
		fmt.Fprintf(os.Stderr, "可以手动安装: curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | sh\n", repoSlug)
		return 1
	}
	if fm.phase == upDone {
		if runtime.GOOS == "windows" {
			fmt.Println("新开的终端会使用新版本；当前窗口里的命令需要重开或重新安装。")
		}
	}
	return 0
}

func (m *upgradeModel) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, func() tea.Msg {
		rel, err := fetchLatestRelease()
		return upgradeCheckMsg{rel: rel, err: err}
	})
}

func (m *upgradeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		w := msg.Width - 16
		if w > 44 {
			w = 44
		}
		if w < 12 {
			w = 12
		}
		m.bar.Width = w
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
		if m.phase == upFailed || m.phase == upLatest || m.phase == upDone {
			return m, tea.Quit
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case upgradeCheckMsg:
		if msg.err != nil {
			m.phase, m.err = upFailed, msg.err
			return m, nil
		}
		m.rel = msg.rel
		m.latest = strings.TrimPrefix(msg.rel.TagName, "v")

		if !m.force && !isNewer(m.latest, version) {
			m.phase = upLatest
			return m, tea.Tick(900*time.Millisecond, func(time.Time) tea.Msg { return upgradeQuitMsg{} })
		}

		asset := assetName(runtime.GOOS, runtime.GOARCH)
		for _, a := range msg.rel.Assets {
			if a.Name == asset {
				m.url = a.URL
				break
			}
		}
		if m.url == "" {
			m.phase = upFailed
			m.err = fmt.Errorf("这个平台没有对应的二进制（%s），请到 %s 手动下载", asset, msg.rel.HTMLURL)
			return m, nil
		}

		m.phase = upDownloading
		m.lastTime = time.Now()
		m.ch = make(chan upgradeProgressMsg, 128)
		return m, tea.Batch(m.downloadCmd(), m.waitProgress())

	case upgradeProgressMsg:
		m.done, m.total = msg.done, msg.total
		now := time.Now()
		if d := now.Sub(m.lastTime).Seconds(); d >= 0.2 {
			m.speed = float64(m.done-m.lastBytes) / d
			m.lastBytes, m.lastTime = m.done, now
		}
		next := m.waitProgress()
		if m.total > 0 {
			return m, tea.Batch(m.bar.SetPercent(float64(m.done)/float64(m.total)), next)
		}
		return m, next

	case upgradeProgressClosedMsg:
		return m, nil

	case progress.FrameMsg:
		barModel, cmd := m.bar.Update(msg)
		if b, ok := barModel.(progress.Model); ok {
			m.bar = b
		}
		return m, cmd

	case upgradeDownloadedMsg:
		if msg.err != nil {
			m.phase, m.err = upFailed, msg.err
			return m, nil
		}
		m.tmp = msg.tmp
		m.phase = upInstalling
		m.bar.SetPercent(1)
		// 换文件本身是毫秒级的，稍等一下让这一帧能被看到
		return m, tea.Tick(220*time.Millisecond, func(time.Time) tea.Msg { return upgradeInstallStartMsg{} })

	case upgradeInstallStartMsg:
		tmp, exe := m.tmp, m.exe
		return m, func() tea.Msg {
			if err := installBinary(tmp, exe); err != nil {
				return upgradeInstalledMsg{err: err}
			}
			refreshSkillAfterUpgrade(exe)
			return upgradeInstalledMsg{}
		}

	case upgradeInstalledMsg:
		if msg.err != nil {
			m.phase, m.err = upFailed, msg.err
			return m, nil
		}
		m.phase = upDone
		return m, tea.Tick(1100*time.Millisecond, func(time.Time) tea.Msg { return upgradeQuitMsg{} })

	case upgradeQuitMsg:
		return m, tea.Quit
	}

	return m, nil
}

func (m *upgradeModel) downloadCmd() tea.Cmd {
	url, dir, ch := m.url, filepath.Dir(m.exe), m.ch
	return func() tea.Msg {
		defer close(ch)
		tmp, err := downloadBinary(url, dir, func(done, total int64) {
			select {
			case ch <- upgradeProgressMsg{done: done, total: total}:
			default: // 界面来不及消费就丢掉这帧，别把下载卡住
			}
		})
		return upgradeDownloadedMsg{tmp: tmp, err: err}
	}
}

func (m *upgradeModel) waitProgress() tea.Cmd {
	ch := m.ch
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return upgradeProgressClosedMsg{}
		}
		return msg
	}
}

func (m *upgradeModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("kube-token") + dimStyle.Render("  升级"))
	b.WriteString("\n\n")

	switch m.phase {
	case upChecking:
		b.WriteString("  " + m.spin.View() + " " + dimStyle.Render("正在检查最新版本…"))
		b.WriteString("\n\n  " + dimStyle.Render("当前 "+version))

	case upDownloading:
		title := "正在下载 " + m.rel.TagName
		b.WriteString("  " + m.spin.View() + " " + textStyle.Render(title))
		b.WriteString("\n\n  " + m.bar.View())
		b.WriteString("\n\n  " + dimStyle.Render(m.progressText()))

	case upInstalling:
		b.WriteString("  " + m.spin.View() + " " + textStyle.Render("正在替换可执行文件…"))
		b.WriteString("\n\n  " + dimStyle.Render(shortenHome(m.exe)))

	case upLatest:
		b.WriteString("  " + goodStyle.Render("✓ 已是最新版本") + dimStyle.Render("  "+version))

	case upDone:
		b.WriteString("  " + goodStyle.Render("✓ 已更新到 "+m.rel.TagName))
		b.WriteString("\n\n  " + dimStyle.Render(shortenHome(m.exe)))

	case upFailed:
		b.WriteString("  " + badStyle.Render("✗ 更新失败"))
		b.WriteString("\n\n  " + warnStyle.Render(m.err.Error()))
		b.WriteString("\n\n" + hints("任意键", "退出"))
	}

	return b.String()
}

func (m *upgradeModel) progressText() string {
	mb := func(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }
	speed := ""
	if m.speed > 0 {
		speed = fmt.Sprintf("  %s/s", mb(int64(m.speed)))
	}
	if m.total > 0 {
		pct := 100 * float64(m.done) / float64(m.total)
		return fmt.Sprintf("%5.1f%%  %s / %s%s", pct, mb(m.done), mb(m.total), speed)
	}
	return mb(m.done) + speed
}
