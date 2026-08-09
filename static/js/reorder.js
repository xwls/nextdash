/**
 * DragReorder - A simple drag-and-drop reordering system using native HTML5 API
 * 
 * Usage:
 * const reorder = new DragReorder({
 *   container: '#my-list',           // Container selector
 *   itemSelector: '.my-item',        // Item selector (optional, defaults to children)
 *   handleSelector: '.drag-handle',       // Mouse handle (optional; defaults to the item)
 *   touchHandleSelector: '.touch-handle', // Touch-only handle (optional; falls back to handleSelector)
 *   touchConfirmMs: 125,                  // Touch confirmation delay (optional)
 *   onReorder: (newOrder) => {            // Callback when order changes
 *     console.log('New order:', newOrder);
 *   }
 * });
 */

class DragReorder {
    constructor(options = {}) {
        this.container = typeof options.container === 'string' 
            ? document.querySelector(options.container) 
            : options.container;
        
        if (!this.container) {
            console.error('DragReorder: Container not found');
            return;
        }

        this.itemSelector = options.itemSelector || null;
        this.handleSelector = options.handleSelector || null;
        this.touchHandleSelector = Object.prototype.hasOwnProperty.call(options, 'touchHandleSelector')
            ? options.touchHandleSelector
            : this.handleSelector;
        this.delegateItemDragOver = Boolean(options.delegateItemDragOver);
        this.touchContainerSelector = options.touchContainerSelector || '.bookmarks-list[data-category-id]';
        this.onReorder = options.onReorder || null;
        this.longPressMs = Number.isFinite(Number(options.longPressMs)) ? Math.max(0, Number(options.longPressMs)) : 0;
        this.touchConfirmMs = Number.isFinite(Number(options.touchConfirmMs))
            ? Math.max(0, Number(options.touchConfirmMs))
            : this.longPressMs;
        this.itemClass = options.itemClass || 'reorder-item';
        this.selected = null;
        this.dragStartMeta = null;
        this.dragStartPosition = null;
        /* 'ontouchstart' in window is true on many desktop Chromes → wrong branch, no HTML5 drag. */
        this.useCoarsePointerDrag = (() => {
            try {
                return typeof navigator !== 'undefined'
                    && Number(navigator.maxTouchPoints) > 0
                    && typeof window.matchMedia === 'function'
                    && window.matchMedia('(pointer: coarse)').matches;
            } catch {
                return false;
            }
        })();
        this.placeholder = null;
        this.mouseDownAt = new WeakMap();
        this.touchPressTimer = null;
        this.touchDragActive = false;
        this.touchStartPoint = null;
        this.touchSourceElement = null;
        
        // Bind handlers
        this.touchStartHandler = (e) => this.touchStart(e);
        this.touchMoveHandler = (e) => this.touchMove(e);
        this.touchEndHandler = (e) => this.touchEnd(e);
        this.touchCancelHandler = (e) => this.touchCancel(e);
        this.mouseDownHandler = (e) => this.mouseDown(e);
        this.mouseUpHandler = (e) => this.mouseUp(e);
        this.preventDrop = (e) => e.preventDefault();
        this.containerDragOverHandler = (e) => this.dragOverContainer(e);
        
        this.init();
    }

    init() {
        // Add reorder-container class to container
        this.container.classList.add('reorder-container');
        if (!window.__dragReorderState) {
            window.__dragReorderState = { selected: null };
        }
        if (!window.__dragReorderState.placeholder) {
            window.__dragReorderState.placeholder = null;
        }
        
        // Initialize items
        this.refreshItems();
    }

    getHandleElement(item, selector) {
        return selector ? item.querySelector(selector) : item;
    }

    getInputElement(item) {
        return this.getHandleElement(
            item,
            this.useCoarsePointerDrag ? this.touchHandleSelector : this.handleSelector
        );
    }

    refreshItems() {
        // Add item class and idle class, make handles draggable or add touch listeners
        this.getAllItems().forEach(item => {
            if (!item.classList.contains(this.itemClass)) {
                item.classList.add(this.itemClass);
            }
            if (!item.classList.contains('is-idle')) {
                item.classList.add('is-idle');
            }
            const element = this.getInputElement(item);
            if (element) {
                if (this.useCoarsePointerDrag) {
                    element.addEventListener('touchstart', this.touchStartHandler, { passive: false });
                    element.addEventListener('touchmove', this.touchMoveHandler, { passive: false });
                    element.addEventListener('touchend', this.touchEndHandler);
                    element.addEventListener('touchcancel', this.touchCancelHandler);
                }
                if (!this.useCoarsePointerDrag) {
                    element.draggable = true;
                    element.ondragstart = (e) => this.dragStart(e);
                    element.ondragend = (e) => this.dragEnd(e);
                    element.addEventListener('mousedown', this.mouseDownHandler);
                    element.addEventListener('mouseup', this.mouseUpHandler);
                    element.addEventListener('mouseleave', this.mouseUpHandler);
                }
            }
            // Add dragover on each item for mouse drag (skipped when dashboard uses document relay for cross-column)
            if (!this.useCoarsePointerDrag && !this.delegateItemDragOver) {
                item.ondragover = (e) => this.dragOver(e);
            }
        });

        if (!this.useCoarsePointerDrag) {
            /* When a document-level relay owns dragover (delegateItemDragOver), the
               container handler must stay off: two handlers moving the same dragged
               row per event pingpong it between positions and flicker the column. */
            this.container.ondragover = this.delegateItemDragOver ? null : this.containerDragOverHandler;
            this.container.ondrop = (e) => e.preventDefault();
        }
    }

    dragStart(e) {
        if (this.longPressMs > 0) {
            const sourceEl = e.currentTarget || e.target;
            const pressStartedAt = this.mouseDownAt.get(sourceEl) || 0;
            if (!pressStartedAt || (Date.now() - pressStartedAt) < this.longPressMs) {
                e.preventDefault();
                return;
            }
        }
        const fromHandle = e.currentTarget && e.currentTarget.closest
            ? e.currentTarget.closest(`.${this.itemClass}`)
            : null;
        const candidate = fromHandle || (e.target && e.target.closest ? e.target.closest(`.${this.itemClass}`) : null);
        // Never start a reorder while the inline editor owns this row.
        if (candidate?.classList?.contains('bookmark-inline-editing')
            || e.target?.closest?.('.bookmark-inline-form')) {
            e.preventDefault();
            return;
        }
        e.dataTransfer.effectAllowed = 'move';
        e.dataTransfer.setData('text/plain', '');
        this.selected = candidate;
        if (!this.selected) {
            e.preventDefault();
            return;
        }
        this.dragStartMeta = this.getItemMeta(this.selected);
        this.dragStartPosition = this.captureDomPosition(this.selected);
        window.__dragReorderState.selected = this.selected;
        this.removeAllPlaceholders();
        this.selected.classList.remove('is-idle');
        this.selected.classList.add('is-draggable');
        document.body.classList.add('bookmark-dragging');

        // Prevent scrolling on touch devices
        this.disablePageScroll();

        // Prevent dropping anywhere else
        document.addEventListener('dragover', this.preventDrop, { passive: false });
    }

    dragOver(e) {
        e.preventDefault();
        const activeSelected = this.getSelectedItem();
        if (!activeSelected) return;

        const targetItem = e.target.closest(`.${this.itemClass}`);
        if (!targetItem || targetItem === activeSelected) return;

        this.ensurePlaceholder();
        targetItem.parentNode.insertBefore(this.placeholder, targetItem);

        if (this.isBefore(activeSelected, targetItem)) {
            targetItem.parentNode.insertBefore(activeSelected, targetItem);
        } else {
            targetItem.parentNode.insertBefore(activeSelected, targetItem.nextSibling);
        }
    }

    dragOverContainer(e) {
        const activeSelected = this.getSelectedItem();
        if (!activeSelected) return;

        const targetItem = e.target.closest(`.${this.itemClass}`);
        if (targetItem) {
            return;
        }

        e.preventDefault();
        if (activeSelected.parentNode !== this.container) {
            this.container.appendChild(activeSelected);
        }
        this.ensurePlaceholder();
        this.container.appendChild(this.placeholder);
    }

    finishDrag({ cancelled = false } = {}) {
        const activeSelected = this.selected;
        if (!activeSelected) {
            this.touchDragActive = false;
            this.cancelTouchPress();
            this.enablePageScroll();
            document.removeEventListener('dragover', this.preventDrop);
            document.body.classList.remove('bookmark-dragging');
            return false;
        }

        if (cancelled) {
            this.restoreDomPosition(activeSelected, this.dragStartPosition);
        }
        const changed = !cancelled && this.hasDomPositionChanged(activeSelected, this.dragStartPosition);
        const reorderDetails = changed ? {
            from: this.dragStartMeta || this.getItemMeta(activeSelected),
            to: this.getItemMeta(activeSelected)
        } : null;

        activeSelected.style.display = '';
        activeSelected.classList.remove('is-draggable');
        activeSelected.classList.add('is-idle');
        if (changed) {
            activeSelected.classList.add('bookmark-move-in');
            requestAnimationFrame(() => {
                setTimeout(() => activeSelected.classList.remove('bookmark-move-in'), 180);
            });
        }
        this.removeAllPlaceholders();
        this.enablePageScroll();
        document.removeEventListener('dragover', this.preventDrop);
        this.selected = null;
        if (window.__dragReorderState?.selected === activeSelected) {
            window.__dragReorderState.selected = null;
        }
        this.dragStartMeta = null;
        this.dragStartPosition = null;
        this.touchDragActive = false;
        this.cancelTouchPress();
        document.body.classList.remove('bookmark-dragging');

        if (changed && this.onReorder && typeof this.onReorder === 'function') {
            this.onReorder(this.getNewOrder(), reorderDetails);
        }
        return changed;
    }

    dragEnd() {
        this.finishDrag();
    }

    mouseDown(e) {
        const sourceEl = e.currentTarget || e.target;
        if (sourceEl) {
            this.mouseDownAt.set(sourceEl, Date.now());
        }
    }

    mouseUp(e) {
        const sourceEl = e.currentTarget || e.target;
        if (sourceEl && this.mouseDownAt.has(sourceEl)) {
            this.mouseDownAt.delete(sourceEl);
        }
    }

    startTouchDrag() {
        this.selected = this.touchSourceElement?.isConnected
            ? this.touchSourceElement.closest(`.${this.itemClass}`)
            : null;
        if (!this.selected
            || this.selected.classList.contains('bookmark-inline-editing')
            || this.touchSourceElement?.closest?.('.bookmark-inline-form')) {
            this.selected = null;
            this.touchDragActive = false;
            this.cancelTouchPress();
            return;
        }
        this.dragStartMeta = this.getItemMeta(this.selected);
        this.dragStartPosition = this.captureDomPosition(this.selected);
        window.__dragReorderState.selected = this.selected;
        this.removeAllPlaceholders();
        this.selected.classList.remove('is-idle');
        this.selected.classList.add('is-draggable');
        this.touchDragActive = true;
        document.body.classList.add('bookmark-dragging');
    }

    cancelTouchPress() {
        if (this.touchPressTimer) {
            clearTimeout(this.touchPressTimer);
            this.touchPressTimer = null;
        }
        this.touchSourceElement = null;
        this.touchStartPoint = null;
    }

    touchStart(e) {
        if (this.touchDragActive || (e.touches && e.touches.length > 1)) {
            this.touchCancel();
            return;
        }
        const touch = e.touches && e.touches[0] ? e.touches[0] : null;
        this.cancelTouchPress();
        this.touchSourceElement = e.currentTarget || e.target;
        this.touchStartPoint = touch ? { x: touch.clientX, y: touch.clientY } : null;
        this.touchDragActive = false;
        if (this.touchConfirmMs > 0) {
            this.touchPressTimer = setTimeout(() => {
                this.touchPressTimer = null;
                this.startTouchDrag();
            }, this.touchConfirmMs);
            return;
        }
        this.startTouchDrag();
    }
    touchMove(e) {
        if (!this.touchDragActive) {
            const touch = e.touches && e.touches[0] ? e.touches[0] : null;
            if (touch && this.touchStartPoint) {
                const dx = Math.abs(touch.clientX - this.touchStartPoint.x);
                const dy = Math.abs(touch.clientY - this.touchStartPoint.y);
                if (dx > 8 || dy > 8) {
                    this.cancelTouchPress();
                }
            }
            return;
        }
        e.preventDefault();
        const activeSelected = this.getSelectedItem();
        if (!activeSelected) return;
        const touch = e.touches[0];
        const pointElement = document.elementFromPoint(touch.clientX, touch.clientY);
        const targetItem = pointElement ? pointElement.closest(`.${this.itemClass}`) : null;
        const targetContainer = pointElement ? pointElement.closest(this.touchContainerSelector) : null;

        if (targetItem) {
            if (targetItem === activeSelected || targetItem.contains(activeSelected)) {
                return;
            }
            const rect = targetItem.getBoundingClientRect();
            const insertAfter = touch.clientY >= rect.top + (rect.height / 2);
            const referenceNode = insertAfter ? targetItem.nextSibling : targetItem;
            targetItem.parentNode.insertBefore(activeSelected, referenceNode);
            return;
        }
        if (targetContainer) {
            const referenceNode = this.getPositionItems(targetContainer)
                .filter((item) => item !== activeSelected)
                .find((item) => {
                    const rect = item.getBoundingClientRect();
                    return touch.clientY < rect.top + (rect.height / 2);
                }) || null;
            targetContainer.insertBefore(activeSelected, referenceNode);
        }
    }

    touchEnd() {
        if (!this.touchDragActive) {
            this.cancelTouchPress();
            return;
        }
        this.finishDrag();
    }

    touchCancel() {
        if (!this.touchDragActive) {
            this.cancelTouchPress();
            return;
        }
        this.finishDrag({ cancelled: true });
    }

    getPositionItems(parent) {
        if (!parent) return [];
        return Array.from(parent.children).filter((child) => child.classList?.contains(this.itemClass));
    }

    captureDomPosition(item) {
        const parent = item?.parentNode || null;
        const siblings = this.getPositionItems(parent);
        const index = siblings.indexOf(item);
        return {
            parent,
            index,
            previousSibling: index > 0 ? siblings[index - 1] : null,
            nextSibling: index >= 0 && index < siblings.length - 1 ? siblings[index + 1] : null
        };
    }

    hasDomPositionChanged(item, snapshot) {
        if (!item || !snapshot?.parent || item.parentNode !== snapshot.parent) {
            return true;
        }
        return this.getPositionItems(snapshot.parent).indexOf(item) !== snapshot.index;
    }

    restoreDomPosition(item, snapshot) {
        if (!item || !snapshot?.parent || !snapshot.parent.isConnected) {
            return;
        }
        if (snapshot.nextSibling?.parentNode === snapshot.parent) {
            snapshot.parent.insertBefore(item, snapshot.nextSibling);
            return;
        }
        if (snapshot.previousSibling?.parentNode === snapshot.parent) {
            snapshot.parent.insertBefore(item, snapshot.previousSibling.nextSibling);
            return;
        }
        const siblings = this.getPositionItems(snapshot.parent).filter((candidate) => candidate !== item);
        snapshot.parent.insertBefore(item, siblings[snapshot.index] || null);
    }

    isBefore(el1, el2) {
        return el1.parentNode === el2.parentNode
            && !!(el1.compareDocumentPosition(el2) & Node.DOCUMENT_POSITION_FOLLOWING);
    }

    disablePageScroll() {
        // Was an unconditional write plus a hardcoded reset to '', which threw
        // away any lock another component was holding. freezeInteraction keeps
        // the touch-action/user-select pinning a drag needs.
        this.scrollLockToken = window.ScrollLock?.acquire('reorder-drag', { freezeInteraction: true })
            ?? null;
    }

    enablePageScroll() {
        if (this.scrollLockToken) {
            window.ScrollLock?.release(this.scrollLockToken);
            this.scrollLockToken = null;
        }
    }

    getAllItems() {
        if (this.itemSelector) {
            return Array.from(this.container.querySelectorAll(this.itemSelector));
        }
        return Array.from(this.container.children);
    }

    getSelectedItem() {
        if (this.selected) {
            return this.selected;
        }
        if (window.__dragReorderState && window.__dragReorderState.selected) {
            return window.__dragReorderState.selected;
        }
        return null;
    }

    getItemMeta(item) {
        if (!item) {
            return { categoryId: '', index: -1 };
        }
        const parent = item.closest('.bookmarks-list[data-category-id]');
        const categoryId = parent ? (parent.getAttribute('data-category-id') || '') : '';
        const siblings = parent ? Array.from(parent.querySelectorAll(`.${this.itemClass}`)) : [];
        return {
            categoryId,
            index: siblings.indexOf(item)
        };
    }

    getNewOrder() {
        const items = this.getAllItems();
        return items.map((item, index) => ({
            element: item,
            index: index,
            dataIndex: item.getAttribute('data-index') || index
        }));
    }

    ensurePlaceholder() {
        if (!window.__dragReorderState.placeholder) {
            const placeholder = document.createElement('div');
            placeholder.className = 'bookmark-drop-placeholder';
            placeholder.setAttribute('aria-hidden', 'true');
            window.__dragReorderState.placeholder = placeholder;
        }
        this.placeholder = window.__dragReorderState.placeholder;
        // Re-trigger the entry animation each time the placeholder moves.
        this.placeholder.style.animation = 'none';
        void this.placeholder.offsetWidth; // force reflow
        this.placeholder.style.animation = '';
    }

    removePlaceholder() {
        const placeholder = window.__dragReorderState ? window.__dragReorderState.placeholder : null;
        if (placeholder && placeholder.parentNode) {
            placeholder.parentNode.removeChild(placeholder);
        }
    }

    removeAllPlaceholders() {
        document.querySelectorAll('.bookmark-drop-placeholder').forEach((node) => {
            if (node && node.parentNode) {
                node.parentNode.removeChild(node);
            }
        });
    }

    // Public method to destroy the instance
    destroy() {
        if (this.selected) {
            this.finishDrag({ cancelled: true });
        } else {
            this.cancelTouchPress();
            this.touchDragActive = false;
            this.dragStartMeta = null;
            this.dragStartPosition = null;
            this.enablePageScroll();
            document.removeEventListener('dragover', this.preventDrop);
        }
        if (!this.container) {
            return;
        }
        this.container.classList.remove('reorder-container');

        // Remove classes and listeners from items
        this.getAllItems().forEach(item => {
            item.classList.remove(this.itemClass, 'is-idle', 'is-draggable');
            const element = this.getInputElement(item);
            if (element) {
                if (this.useCoarsePointerDrag) {
                    element.removeEventListener('touchstart', this.touchStartHandler);
                    element.removeEventListener('touchmove', this.touchMoveHandler);
                    element.removeEventListener('touchend', this.touchEndHandler);
                    element.removeEventListener('touchcancel', this.touchCancelHandler);
                }
                if (!this.useCoarsePointerDrag) {
                    element.draggable = false;
                    element.ondragstart = null;
                    element.ondragend = null;
                    element.removeEventListener('mousedown', this.mouseDownHandler);
                    element.removeEventListener('mouseup', this.mouseUpHandler);
                    element.removeEventListener('mouseleave', this.mouseUpHandler);
                }
            }
            if (!this.useCoarsePointerDrag && !this.delegateItemDragOver) {
                item.ondragover = null;
            }
        });

        if (!this.useCoarsePointerDrag) {
            this.container.ondragover = null;
            this.container.ondrop = null;
        }
    }
}

// Export for use in other modules
if (typeof module !== 'undefined' && module.exports) {
    module.exports = DragReorder;
}
