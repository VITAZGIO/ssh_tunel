package io.github.vitazgio.sshtunnel

import android.annotation.SuppressLint
import android.content.Context
import android.util.AttributeSet
import android.view.MotionEvent
import android.view.View
import android.widget.ScrollView

/**
 * Главный экран: две страницы одна под другой, но пальцем (и колёсиком
 * мыши) он не листается вовсе — ни рывком, ни с доводкой. Между страницами
 * переходят только кнопками «Устройства» / «Наверх» и логотипом, плавной
 * прокруткой из кода (smoothScrollTo работает как обычно). Касания
 * проходят к кнопкам, а вложенный список устройств листается сам по себе —
 * остаток его прокрутки сюда не передаётся.
 */
class LockedScrollView @JvmOverloads constructor(
    context: Context, attrs: AttributeSet? = null,
) : ScrollView(context, attrs) {

    override fun onInterceptTouchEvent(ev: MotionEvent): Boolean = false

    @SuppressLint("ClickableViewAccessibility")
    override fun onTouchEvent(ev: MotionEvent): Boolean = false

    override fun onGenericMotionEvent(event: MotionEvent): Boolean = false

    override fun onStartNestedScroll(child: View, target: View, nestedScrollAxes: Int): Boolean = false

    override fun onNestedPreFling(target: View, velocityX: Float, velocityY: Float): Boolean = false

    override fun onNestedFling(target: View, velocityX: Float, velocityY: Float, consumed: Boolean): Boolean = false
}
