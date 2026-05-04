// Adds a prominent "← yoebuild.org" link to the mdBook menu bar
// and makes the book title a link back to the home page.
(function () {
  var HOME_URL = 'https://yoebuild.org/';

  function init() {
    // Make the book title clickable
    var title = document.querySelector('.menu-title');
    if (title && !title.querySelector('a')) {
      var text = title.textContent.trim();
      title.textContent = '';
      var titleLink = document.createElement('a');
      titleLink.href = HOME_URL;
      titleLink.textContent = text;
      titleLink.style.color = 'inherit';
      titleLink.style.textDecoration = 'none';
      title.appendChild(titleLink);
    }

    // Add a prominent "← yoebuild.org" link to the menu bar
    var menuBar = document.getElementById('menu-bar');
    if (menuBar && !document.getElementById('home-link')) {
      var rightButtons = menuBar.querySelector('.right-buttons');
      var homeLink = document.createElement('a');
      homeLink.id = 'home-link';
      homeLink.href = HOME_URL;
      homeLink.textContent = '← yoebuild.org';
      homeLink.title = 'Back to yoebuild.org';
      homeLink.style.cssText = [
        'color: var(--icons)',
        'padding: 0 1rem',
        'font-size: 0.9rem',
        'text-decoration: none',
        'line-height: 50px',
        'white-space: nowrap',
      ].join(';');
      homeLink.addEventListener('mouseover', function () {
        homeLink.style.color = 'var(--icons-hover)';
      });
      homeLink.addEventListener('mouseout', function () {
        homeLink.style.color = 'var(--icons)';
      });
      if (rightButtons) {
        menuBar.insertBefore(homeLink, rightButtons);
      } else {
        menuBar.appendChild(homeLink);
      }
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
