function init() {
  document.addEventListener('click', function() {
    onClick();
  });
  setTimeout(function() {
    onTimeout();
  }, 100);
  fetch('/api/test').then(function() {
    onThen();
  }).catch(function() {
    onCatch();
  });
}

function onClick() {}
function onTimeout() {}
function onThen() {}
function onCatch() {}
