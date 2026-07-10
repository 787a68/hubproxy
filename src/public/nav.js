// escapeHtml 转义 HTML 特殊字符，防止 XSS
function escapeHtml(str) {
    if (str == null) return '';
    return String(str)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

// showToast 显示 toast 提示
function showToast(message) {
    const toast = document.getElementById('toast');
    if (!toast) return;
    toast.textContent = message;
    toast.classList.add('show');
    setTimeout(() => { toast.classList.remove('show'); }, 3000);
}

// copyToClipboard 复制文本到剪贴板，带降级方案
function copyToClipboard(text) {
    if (navigator.clipboard) {
        navigator.clipboard.writeText(text).then(
            () => showToast('已复制到剪贴板'),
            () => showToast('复制失败')
        );
    } else {
        const textarea = document.createElement('textarea');
        textarea.value = text;
        textarea.style.position = 'fixed';
        textarea.style.opacity = '0';
        document.body.appendChild(textarea);
        textarea.select();
        try {
            document.execCommand('copy');
            showToast('已复制到剪贴板');
        } catch (e) {
            showToast('复制失败');
        }
        document.body.removeChild(textarea);
    }
}

// renderNavbar 动态生成导航栏 HTML，根据当前路径标记 active
function renderNavbar() {
    const path = window.location.pathname;
    const links = [
        { href: '/', icon: '🚀', text: 'GitHub加速' },
        { href: '/images.html', icon: '🐳', text: '离线镜像下载' },
        { href: '/search.html', icon: '🔍', text: '镜像搜索' },
    ];
    const linksHtml = links.map(l =>
        `<a href="${l.href}" class="nav-link${path === l.href ? ' active' : ''}">${l.icon} ${l.text}</a>`
    ).join('');
    return `
        <nav class="navbar">
            <div class="navbar-container">
                <a href="/" class="logo"><div class="logo-icon">⚡</div>加速服务</a>
                <button class="mobile-menu-toggle" id="mobileMenuToggle">☰</button>
                <div class="nav-links" id="navLinks">${linksHtml}</div>
            </div>
        </nav>
    `;
}

// initNav 初始化导航栏：动态生成 + 移动端菜单切换
function initNav() {
    // 动态插入导航栏
    const navPlaceholder = document.getElementById('nav-placeholder');
    if (navPlaceholder) {
        navPlaceholder.innerHTML = renderNavbar();
    }

    const mobileMenuToggle = document.getElementById('mobileMenuToggle');
    const navLinks = document.getElementById('navLinks');

    if (mobileMenuToggle && navLinks) {
        mobileMenuToggle.addEventListener('click', () => {
            navLinks.classList.toggle('active');
            mobileMenuToggle.textContent = navLinks.classList.contains('active') ? '✕' : '☰';
        });

        document.addEventListener('click', (e) => {
            if (!e.target.closest('.navbar') && navLinks.classList.contains('active')) {
                navLinks.classList.remove('active');
                mobileMenuToggle.textContent = '☰';
            }
        });
    }
}
