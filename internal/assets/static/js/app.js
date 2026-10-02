/* SoulFound ERP · 前端交互
   - PWA 安装引导与服务注册
   - 在线/离线状态提示
   - 市集录入的断网自动重试（市集现场信号差时最实用）
   - 局部刷新后恢复输入焦点
*/
(function () {
  'use strict';

  /* ---------------------------------------------------------------- PWA */
  var installBar = document.getElementById('install-bar');
  var installBtn = document.getElementById('install-btn');
  var installDismiss = document.getElementById('install-dismiss');
  var deferredPrompt = null;
  var DISMISS_KEY = 'erp_install_dismissed_at';

  function dismissedRecently() {
    try {
      var at = parseInt(localStorage.getItem(DISMISS_KEY) || '0', 10);
      return at && Date.now() - at < 1000 * 60 * 60 * 24 * 14;
    } catch (e) {
      return false;
    }
  }

  function showInstallBar() {
    if (!installBar || dismissedRecently()) return;
    // 已经是独立窗口（已安装）就不再提示
    var standalone = window.matchMedia('(display-mode: standalone)').matches ||
      window.navigator.standalone === true;
    if (standalone) return;
    installBar.classList.add('is-visible');
  }

  window.addEventListener('beforeinstallprompt', function (e) {
    e.preventDefault();
    deferredPrompt = e;
    showInstallBar();
  });

  if (installBtn) {
    installBtn.addEventListener('click', function () {
      if (!deferredPrompt) {
        alert('请用浏览器菜单里的「添加到主屏幕」来完成安装。\niOS Safari：点底部分享按钮 → 添加到主屏幕。');
        return;
      }
      deferredPrompt.prompt();
      deferredPrompt.userChoice.then(function () {
        deferredPrompt = null;
        if (installBar) installBar.classList.remove('is-visible');
      });
    });
  }
  if (installDismiss) {
    installDismiss.addEventListener('click', function () {
      if (installBar) installBar.classList.remove('is-visible');
      try { localStorage.setItem(DISMISS_KEY, String(Date.now())); } catch (e) {}
    });
  }

  window.addEventListener('appinstalled', function () {
    if (installBar) installBar.classList.remove('is-visible');
  });

  if ('serviceWorker' in navigator) {
    window.addEventListener('load', function () {
      navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(function () {});
    });
  }

  /* ------------------------------------------------------------ 在线状态 */
  var offlineBanner = document.getElementById('offline-banner');
  function syncOnline() {
    if (!offlineBanner) return;
    if (navigator.onLine) {
      offlineBanner.classList.remove('is-visible');
    } else {
      offlineBanner.classList.add('is-visible');
    }
  }
  window.addEventListener('online', syncOnline);
  window.addEventListener('offline', syncOnline);
  syncOnline();

  /* --------------------------------------------------- 保存状态与失败重试 */
  var MAX_RETRY = 5;

  function setState(elt, state, text) {
    if (!elt) return;
    var row = elt.closest('.entry-row') || elt;
    var badge = row.querySelector('.save-state');
    row.classList.remove('is-saving', 'is-saved', 'is-error');
    if (state === 'saving') row.classList.add('is-saving');
    if (state === 'saved') row.classList.add('is-saved');
    if (state === 'error') row.classList.add('is-error');
    if (badge && text) badge.textContent = text;
  }

  function isRetryable(elt) {
    if (!elt) return false;
    if (elt.dataset && elt.dataset.retryable === '1') return true;
    var row = elt.closest && elt.closest('[data-retryable="1"]');
    return !!row;
  }

  function scheduleRetry(elt) {
    var target = isRetryable(elt) ? (elt.closest('[data-retryable="1"]') || elt) : null;
    if (!target) return;
    var n = parseInt(target.dataset.retryCount || '0', 10);
    if (n >= MAX_RETRY) {
      setState(target, 'error', '保存失败，请检查网络后重新输入');
      return;
    }
    target.dataset.retryCount = String(n + 1);
    var delay = Math.min(1000 * Math.pow(2, n), 15000);
    setState(target, 'saving', '网络中断，' + Math.round(delay / 1000) + ' 秒后自动重试…');
    window.setTimeout(function () {
      if (!navigator.onLine) {
        scheduleRetry(target);
        return;
      }
      if (window.htmx) window.htmx.trigger(target, 'change');
    }, delay);
  }

  document.addEventListener('htmx:sendError', function (e) {
    scheduleRetry(e.detail && e.detail.elt);
  });

  document.addEventListener('htmx:responseError', function (e) {
    var elt = e.detail && e.detail.elt;
    if (isRetryable(elt)) {
      scheduleRetry(elt);
      return;
    }
    if (e.detail && e.detail.xhr && e.detail.xhr.status === 403) {
      alert('当前账号没有执行该操作的权限。');
      return;
    }
    // 其他错误：提示刷新
    var row = elt && elt.closest ? elt.closest('.entry-row') : null;
    if (row) setState(row, 'error', '操作失败，请刷新页面重试');
  });

  document.addEventListener('htmx:afterRequest', function (e) {
    var elt = e.detail && e.detail.elt;
    var target = elt && elt.closest ? (elt.closest('[data-retryable="1"]') || elt) : null;
    if (target && e.detail.successful) {
      target.dataset.retryCount = '0';
      setState(target, 'saved');
      window.setTimeout(function () { setState(target, 'idle'); }, 1600);
    }
  });

  /* --------------------------------------------------- 刷新后恢复输入焦点 */
  var lastFocus = null;

  document.addEventListener('htmx:beforeRequest', function () {
    var a = document.activeElement;
    if (a && a.name) {
      var row = a.closest('[data-item-id]');
      lastFocus = { name: a.name, itemId: row ? row.dataset.itemId : null };
    }
  });

  document.addEventListener('htmx:afterSwap', function () {
    if (!lastFocus) return;
    var sel = lastFocus.itemId
      ? '[data-item-id="' + lastFocus.itemId + '"] [name="' + lastFocus.name + '"]'
      : '[name="' + lastFocus.name + '"]';
    var el = document.querySelector(sel);
    lastFocus = null;
    if (!el) return;
    el.focus();
    if (el.setSelectionRange && (el.type === 'text' || el.type === 'search')) {
      try { el.setSelectionRange(el.value.length, el.value.length); } catch (e) {}
    }
  });

  /* ------------------------------------------------------ 提示消息自动消失 */
  window.setTimeout(function () {
    document.querySelectorAll('.flash-stack .alert--success').forEach(function (el) {
      el.style.transition = 'opacity .4s';
      el.style.opacity = '0';
      window.setTimeout(function () { el.remove(); }, 420);
    });
  }, 5000);

  /* ------------------------------------------- 表单重复提交与按钮加载状态 */
  document.addEventListener('submit', function (e) {
    var form = e.target;
    if (!form || form.dataset.submitted === '1') {
      if (form && form.dataset.submitted === '1') e.preventDefault();
      return;
    }
    form.dataset.submitted = '1';
    var btn = form.querySelector('button[type="submit"]');
    if (btn) {
      btn.disabled = true;
      var original = btn.innerHTML;
      btn.innerHTML = '<span>处理中…</span>';
      window.setTimeout(function () {
        form.dataset.submitted = '0';
        btn.disabled = false;
        btn.innerHTML = original;
      }, 6000);
    }
  });

  /* ------------------------------------------------- 数字输入整页回车提交 */
  document.addEventListener('keydown', function (e) {
    if (e.key !== 'Enter') return;
    var el = e.target;
    if (!el || el.tagName !== 'INPUT') return;
    // 市集录入行内回车：立刻触发本行保存
    var row = el.closest('[data-retryable="1"]');
    if (row && window.htmx) {
      e.preventDefault();
      el.blur();
      window.htmx.trigger(row, 'change');
    }
  });
})();
