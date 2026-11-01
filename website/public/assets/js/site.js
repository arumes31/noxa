(() => {
  'use strict';

  const menu = document.querySelector('.menu-button');
  const nav = document.querySelector('#navigation');
  const mobile = window.matchMedia('(max-width: 900px)');
  const guideDetails = document.querySelector('.docs-sidebar details');
  if (guideDetails) {
    const syncGuides = () => { guideDetails.open = !mobile.matches; };
    mobile.addEventListener('change', syncGuides);
    syncGuides();
  }

  if (menu && nav) {
    const setOpen = (open, restoreFocus = false) => {
      menu.setAttribute('aria-expanded', String(open));
      menu.setAttribute('aria-label', open ? 'Close navigation' : 'Open navigation');
      nav.hidden = mobile.matches && !open;
      if (restoreFocus) menu.focus();
    };
    const syncLayout = () => {
      // Keep the complete navigation available if JavaScript never runs.
      menu.hidden = !mobile.matches;
      setOpen(false);
    };
    menu.addEventListener('click', () => setOpen(menu.getAttribute('aria-expanded') !== 'true'));
    nav.addEventListener('click', (event) => {
      if (event.target.closest('a') && mobile.matches) setOpen(false);
    });
    document.addEventListener('keydown', (event) => {
      if (event.key === 'Escape' && menu.getAttribute('aria-expanded') === 'true') {
        setOpen(false, true);
      }
    });
    mobile.addEventListener('change', syncLayout);
    syncLayout();
  }

  const dialog = document.querySelector('.lightbox');
  if (!dialog || typeof dialog.showModal !== 'function') return;
  let image;
  let opener;

  document.querySelectorAll('a[data-full]').forEach((link) => {
    link.addEventListener('click', (event) => {
      if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
      event.preventDefault();
      opener = link;
      if (!image) {
        image = document.createElement('img');
        dialog.insertBefore(image, dialog.querySelector('#image-caption'));
      }
      image.src = link.href;
      image.alt = link.querySelector('img').alt;
      const caption = link.closest('figure')?.querySelector('figcaption');
      dialog.querySelector('#image-caption').textContent =
        (caption?.querySelector('span') || caption)?.textContent.trim() || image.alt;
      dialog.showModal();
    });
  });
  dialog.querySelector('button').addEventListener('click', () => dialog.close());
  dialog.addEventListener('keydown', (event) => {
    if (event.key === 'Tab') {
      // The close button is the only interactive element in the image dialog.
      event.preventDefault();
      dialog.querySelector('button').focus();
    }
  });
  dialog.addEventListener('close', () => opener?.focus({ preventScroll: true }));
  dialog.addEventListener('click', (event) => {
    if (event.target !== dialog) return;
    const bounds = dialog.getBoundingClientRect();
    if (event.clientX < bounds.left || event.clientX > bounds.right ||
        event.clientY < bounds.top || event.clientY > bounds.bottom) dialog.close();
  });
})();
