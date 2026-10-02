/* SoulFound ERP · 扫码模块
 *
 * 用法（写在按钮上即可，不需要额外脚本）：
 *   data-scan-submit="#表单选择器"   识别后把条码填进表单的 code 字段并提交（HTMX 或普通表单）
 *   data-scan-fill="#输入框选择器"   识别后填进输入框（产品档案"扫码录入条码"）
 *
 * 摄像头只在"安全上下文"下可用：https:// 或 localhost。
 * 局域网 http://IP:8123 会被浏览器直接拒绝，这里会给出明确提示并允许手动输入条码。
 */
(function () {
  'use strict';

  var overlay = null;
  var videoEl = null;
  var reader = null;
  var activeTrack = null;
  var lastText = '';
  var lastAt = 0;
  var pending = null;

  function isSecure() {
    return window.isSecureContext === true ||
      location.protocol === 'https:' ||
      location.hostname === 'localhost' ||
      location.hostname === '127.0.0.1' ||
      location.hostname === '[::1]';
  }

  function hasCameraApi() {
    return !!(navigator.mediaDevices && navigator.mediaDevices.getUserMedia);
  }

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    if (text) node.textContent = text;
    return node;
  }

  function buildOverlay() {
    var root = el('div', 'scan-modal');
    root.setAttribute('role', 'dialog');
    root.setAttribute('aria-label', '扫码');

    var box = el('div', 'scan-modal__box');

    var head = el('div', 'scan-modal__head');
    head.appendChild(el('div', 'scan-modal__title', '扫描条形码'));
    var closeBtn = el('button', 'btn btn--ghost btn--icon', '');
    closeBtn.type = 'button';
    closeBtn.setAttribute('aria-label', '关闭');
    closeBtn.innerHTML = '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>';
    head.appendChild(closeBtn);
    box.appendChild(head);

    var stage = el('div', 'scan-modal__stage');
    var video = document.createElement('video');
    video.setAttribute('playsinline', '');
    video.setAttribute('muted', '');
    video.muted = true;
    video.className = 'scan-modal__video';
    stage.appendChild(video);
    var frame = el('div', 'scan-modal__frame');
    stage.appendChild(frame);
    box.appendChild(stage);

    var status = el('div', 'scan-modal__status', '正在启动摄像头…');
    box.appendChild(status);

    var manual = el('div', 'scan-modal__manual');
    manual.appendChild(el('div', 'scan-modal__manual-label', '读不出来？直接手动输入条码：'));
    var row = el('div', 'scan-modal__row');
    var input = document.createElement('input');
    input.className = 'input';
    input.type = 'text';
    input.inputMode = 'numeric';
    input.autocomplete = 'off';
    input.placeholder = '输入或粘贴条码';
    var ok = el('button', 'btn btn--primary', '确定');
    ok.type = 'button';
    row.appendChild(input);
    row.appendChild(ok);
    manual.appendChild(row);
    box.appendChild(manual);

    var actions = el('div', 'scan-modal__actions');
    var torch = el('button', 'btn btn--sm', '开闪光灯');
    torch.type = 'button';
    torch.style.display = 'none';
    var switchBtn = el('button', 'btn btn--sm', '切换摄像头');
    switchBtn.type = 'button';
    switchBtn.style.display = 'none';
    actions.appendChild(torch);
    actions.appendChild(switchBtn);
    box.appendChild(actions);

    root.appendChild(box);
    document.body.appendChild(root);

    var api = {
      root: root, video: video, status: status, input: input,
      close: closeBtn, ok: ok, torch: torch, switchBtn: switchBtn,
      frame: frame
    };
    closeBtn.addEventListener('click', function () { close(); });
    root.addEventListener('click', function (e) { if (e.target === root) close(); });
    ok.addEventListener('click', function () { submitText(input.value); });
    input.addEventListener('keydown', function (e) {
      if (e.key === 'Enter') { e.preventDefault(); submitText(input.value); }
    });
    return api;
  }

  function setStatus(text, kind) {
    if (!overlay) return;
    overlay.status.textContent = text;
    overlay.status.className = 'scan-modal__status' + (kind ? ' scan-modal__status--' + kind : '');
  }

  function beep() {
    if (navigator.vibrate) {
      try { navigator.vibrate(60); } catch (err) { /* 忽略 */ }
    }
    if (overlay) {
      overlay.frame.classList.add('scan-modal__frame--hit');
      window.setTimeout(function () {
        if (overlay) overlay.frame.classList.remove('scan-modal__frame--hit');
      }, 220);
    }
  }

  function submitText(raw) {
    var code = (raw || '').trim();
    if (!code) { setStatus('请输入条码内容', 'warn'); return; }
    var opts = pending || {};
    close();
    if (opts.onResult) {
      opts.onResult(code);
      return;
    }
    if (opts.submitSelector) {
      var form = document.querySelector(opts.submitSelector);
      if (!form) return;
      applyToForm(form, code);
      return;
    }
    if (opts.fillSelector) {
      var target = document.querySelector(opts.fillSelector);
      if (!target) return;
      target.value = code;
      target.dispatchEvent(new Event('input', { bubbles: true }));
      target.dispatchEvent(new Event('change', { bubbles: true }));
      target.focus();
    }
  }

  function applyToForm(form, code) {
    var input = form.querySelector('input[name="code"]');
    if (!input) {
      input = document.createElement('input');
      input.type = 'hidden';
      input.name = 'code';
      form.appendChild(input);
    }
    input.value = code;
    if (window.htmx) {
      window.htmx.trigger(form, 'submit');
      return;
    }
    form.submit();
  }

  // handleDecoded 统一处理识别结果：防重复、震动反馈、交给回调
  function handleDecoded(result) {
    var text = '';
    try {
      text = (result.getText ? result.getText() : String(result)).trim();
    } catch (err) {
      text = '';
    }
    if (!text) return;
    var now = Date.now();
    if (text === lastText && now - lastAt < 1500) return; // 同一瓶不要连记多次
    lastText = text;
    lastAt = now;
    beep();
    submitText(text);
  }

  function stopCamera() {
    if (reader) {
      try { reader.reset(); } catch (err) { /* 忽略 */ }
      reader = null;
    }
    if (activeTrack) {
      try { activeTrack.stop(); } catch (err) { /* 忽略 */ }
      activeTrack = null;
    }
    if (videoEl) {
      try { videoEl.srcObject = null; } catch (err) { /* 忽略 */ }
    }
  }

  function close() {
    document.body.classList.remove('scan-open');
    stopCamera();
    if (overlay) {
      overlay.root.remove();
      overlay = null;
    }
    videoEl = null;
    pending = null;
  }

  function startCamera(facing) {
    if (!window.ZXing || !ZXing.BrowserMultiFormatReader) {
      setStatus('扫码组件没有加载成功，请刷新页面重试；也可以手动输入条码。', 'warn');
      return;
    }
    stopCamera();

    var hints = new Map();
    if (ZXing.DecodeHintType && ZXing.BarcodeFormat) {
      hints.set(ZXing.DecodeHintType.POSSIBLE_FORMATS, [
        ZXing.BarcodeFormat.EAN_13, ZXing.BarcodeFormat.EAN_8,
        ZXing.BarcodeFormat.UPC_A, ZXing.BarcodeFormat.UPC_E,
        ZXing.BarcodeFormat.CODE_128, ZXing.BarcodeFormat.CODE_39,
        ZXing.BarcodeFormat.ITF, ZXing.BarcodeFormat.QR_CODE
      ]);
      hints.set(ZXing.DecodeHintType.TRY_HARDER, true);
    }

    try {
      reader = new ZXing.BrowserMultiFormatReader(hints, 350);
    } catch (err) {
      reader = new ZXing.BrowserMultiFormatReader();
    }

    var constraints = {
      video: {
        facingMode: facing === 'user' ? 'user' : { ideal: 'environment' },
        width: { ideal: 1280 },
        height: { ideal: 720 }
      },
      audio: false
    };

    var started = reader.decodeFromConstraints(constraints, overlay.video, function (result) {
      if (!result) return;
      handleDecoded(result);
    });

    if (!started || !started.then) {
      setStatus('摄像头已启动，把条形码放进取景框内', 'live');
      return;
    }

    started.then(function () {
      setStatus('把条形码放进取景框内，识别成功会自动记录', 'live');

      // 从 video 上取回轨道，用于闪光灯
      var stream = overlay.video.srcObject;
      activeTrack = stream && stream.getVideoTracks ? (stream.getVideoTracks()[0] || null) : null;
      if (activeTrack && activeTrack.getCapabilities) {
        var caps = {};
        try { caps = activeTrack.getCapabilities() || {}; } catch (err) { caps = {}; }
        if (caps.torch) {
          overlay.torch.style.display = '';
          overlay.torch.onclick = function () {
            var on = overlay.torch.classList.toggle('is-on');
            overlay.torch.textContent = on ? '关闪光灯' : '开闪光灯';
            try {
              activeTrack.applyConstraints({ advanced: [{ torch: on }] });
            } catch (err) { /* 不支持就忽略 */ }
          };
        }
      }

      // 多个摄像头时给出切换入口
      if (navigator.mediaDevices && navigator.mediaDevices.enumerateDevices) {
        navigator.mediaDevices.enumerateDevices().then(function (list) {
          var devices = list.filter(function (d) { return d.kind === 'videoinput'; });
          if (devices.length < 2) return;
          overlay.switchBtn.style.display = '';
          var idx = 0;
          overlay.switchBtn.onclick = function () {
            idx = (idx + 1) % devices.length;
            startWithDevice(devices[idx].deviceId);
          };
        }).catch(function () { /* 忽略 */ });
      }
    }).catch(function (err) {
      var name = (err && err.name) || '';
      if (name === 'NotAllowedError' || name === 'SecurityError') {
        setStatus('摄像头权限被拒绝。请在浏览器地址栏的权限设置里允许摄像头后重试，也可以手动输入条码。', 'error');
      } else if (name === 'NotFoundError' || name === 'OverconstrainedError') {
        setStatus('没有找到可用的摄像头。可以手动输入条码。', 'error');
      } else if (name === 'NotReadableError') {
        setStatus('摄像头被其他程序占用了（比如相机 App），关掉后再试。', 'error');
      } else {
        setStatus('无法启动摄像头：' + (err && err.message ? err.message : name) + '。可以手动输入条码。', 'error');
      }
      if (overlay) overlay.input.focus();
    });
  }

  function startWithDevice(deviceId) {
    if (!reader || !overlay) return;
    stopCamera();
    try {
      reader = new ZXing.BrowserMultiFormatReader();
    } catch (err) { /* 忽略 */ }
    reader.decodeFromVideoDevice(deviceId, overlay.video, function (result) {
      if (!result) return;
      handleDecoded(result);
    });
  }

  function open(opts) {
    if (overlay) close();
    pending = opts || {};
    overlay = buildOverlay();
    document.body.classList.add('scan-open');

    if (!hasCameraApi()) {
      setStatus('这个浏览器不支持调用摄像头。请手动输入条码，或换用 Chrome / Safari。', 'error');
      overlay.input.focus();
      return;
    }
    if (!isSecure()) {
      setStatus('浏览器只允许在 HTTPS 下调用摄像头（当前是 http://' + location.host + '）。请在反向代理上配置证书后重试，或手动输入条码。', 'error');
      overlay.input.focus();
      return;
    }
    startCamera('environment');
  }

  document.addEventListener('click', function (e) {
    var btn = e.target.closest('[data-scan-submit],[data-scan-fill],[data-scan-lookup]');
    if (!btn) return;
    e.preventDefault();
    var opts = {
      submitSelector: btn.getAttribute('data-scan-submit'),
      fillSelector: btn.getAttribute('data-scan-fill')
    };
    opts.onResult = function (code) {
      if (opts.submitSelector) {
        var form = document.querySelector(opts.submitSelector);
        if (form) applyToForm(form, code);
        return;
      }
      if (opts.fillSelector) {
        var target = document.querySelector(opts.fillSelector);
        if (target) {
          target.value = code;
          target.dispatchEvent(new Event('input', { bubbles: true }));
          target.dispatchEvent(new Event('change', { bubbles: true }));
        }
      }
    };
    open(opts);
  });

  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && overlay) close();
  });

  window.ERPScan = { open: open, close: close, isSecure: isSecure, hasCameraApi: hasCameraApi };
})();
