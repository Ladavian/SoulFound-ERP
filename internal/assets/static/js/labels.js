/* SoulFound ERP · 条码标签打印
 *
 * 用 JsBarcode 把每个产品的条码（没录条码就用 SKU）渲染成 Code128，
 * 贴到瓶子上之后，收银台扫码即可直接出库。
 *
 * 关键处理：JsBarcode 输出的是固定像素宽高的 SVG，没有 viewBox 时
 * 用 CSS 缩放只会被裁掉。这里补上 viewBox 后再交给 CSS，标签纸上才能
 * 自适应宽度而不失真。
 */
(function () {
  'use strict';

  function render() {
    var nodes = document.querySelectorAll('svg[data-code]');
    if (!nodes.length) return;

    if (!window.JsBarcode) {
      var warn = document.getElementById('label-warning');
      if (warn) warn.style.display = '';
      return;
    }

    Array.prototype.forEach.call(nodes, function (el) {
      var code = (el.getAttribute('data-code') || '').trim();
      if (!code) {
        el.closest('.label').classList.add('label--empty');
        return;
      }
      try {
        JsBarcode(el, code, {
          format: 'CODE128',
          width: 1.4,
          height: 44,
          displayValue: false,
          margin: 0
        });
        var w = el.getAttribute('width');
        var h = el.getAttribute('height');
        if (w && h) {
          el.setAttribute('viewBox', '0 0 ' + w + ' ' + h);
          el.removeAttribute('width');
          el.removeAttribute('height');
        }
      } catch (err) {
        el.closest('.label').classList.add('label--error');
      }
    });
  }

  document.addEventListener('DOMContentLoaded', function () {
    render();
    var btn = document.getElementById('label-print');
    if (btn) {
      btn.addEventListener('click', function () {
        window.print();
      });
    }
    var count = document.querySelectorAll('.label').length;
    var tip = document.getElementById('label-count');
    if (tip) tip.textContent = String(count);
  });
})();
