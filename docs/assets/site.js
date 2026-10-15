(function () {
  var reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  // Copy buttons: copy the text of the element named by data-copy.
  document.querySelectorAll('[data-copy]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var src = document.getElementById(btn.getAttribute('data-copy'));
      if (!src || !navigator.clipboard) return;
      navigator.clipboard.writeText(src.textContent).then(function () {
        btn.classList.add('is-done');
        clearTimeout(btn._t);
        btn._t = setTimeout(function () { btn.classList.remove('is-done'); }, 1800);
      });
    });
  });

  // The hero terminal runs once, the first time it comes into view.
  var term = document.querySelector('[data-play]');
  if (!term || reduce || document.visibilityState !== 'visible' || !('IntersectionObserver' in window)) return;
  var lines = Array.prototype.slice.call(term.querySelectorAll('.l'));
  var notes = document.querySelector('.notes');
  var noteAt = {};
  if (notes) {
    notes.classList.add('is-waiting');
    notes.querySelectorAll('[data-after]').forEach(function (n) { noteAt[n.getAttribute('data-after')] = n; });
  }
  term.classList.add('is-playing');

  function play() {
    term.classList.add('is-live');
    var i = 0;
    (function next() {
      if (i >= lines.length) {
        setTimeout(function () { term.classList.remove('is-live', 'is-playing'); }, 700);
        return;
      }
      var line = lines[i];
      line.classList.add('on');
      if (noteAt[i]) noteAt[i].classList.add('on');
      i++;
      var wait = line.classList.contains('cmd') ? 520 : (lines[i] && lines[i].classList.contains('cmd') ? 620 : 70);
      setTimeout(next, wait);
    })();
  }

  var io = new IntersectionObserver(function (entries) {
    if (entries[0].isIntersecting) { io.disconnect(); setTimeout(play, 450); }
  }, { threshold: 0.35 });
  io.observe(term);
})();
