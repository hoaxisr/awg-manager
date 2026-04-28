<script lang="ts">
  import { onMount } from 'svelte';

  interface Props {
    history: number[];
    width?: number;
    height?: number;
    colorOk?: string;
    colorSlow?: string;
    colorFail?: string;
    thresholdOk?: number;
    thresholdSlow?: number;
    onclick?: () => void;
  }

  let {
    history = [],
    width = 120,
    height = 26,
    colorOk = '#10b981',
    colorSlow = '#f59e0b',
    colorFail = '#ef4444',
    thresholdOk = 200,
    thresholdSlow = 500,
    onclick
  }: Props = $props();

  let canvas: HTMLCanvasElement;

  function draw() {
    if (!canvas) return;
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    const w = canvas.width;
    const h = canvas.height;
    ctx.clearRect(0, 0, w, h);

    if (history.length === 0) return;

    const step = w / (history.length - 1);
    const validValues = history.map(v => v <= 0 ? thresholdSlow : v);
    const maxVal = validValues.length > 0 ? Math.max(...validValues, thresholdSlow) : thresholdSlow;
    const points = history.map((v, i) => ({
      x: i * step,
      y: h - (v > 0 ? (v / maxVal) * h : 2),
    }));

    // Рисуем линию
    ctx.beginPath();
    ctx.moveTo(points[0].x, points[0].y);
    for (let i = 1; i < points.length; i++) {
      ctx.lineTo(points[i].x, points[i].y);
    }
    ctx.strokeStyle = history[history.length-1] < thresholdOk ? colorOk : (history[history.length-1] < thresholdSlow ? colorSlow : colorFail);
    ctx.lineWidth = 1.5;
    ctx.stroke();

    // Точки
    for (let i = 0; i < points.length; i++) {
      ctx.beginPath();
      ctx.arc(points[i].x, points[i].y, 1.2, 0, 2*Math.PI);
      ctx.fillStyle = i === points.length-1 ? '#ffffff' : (history[i] < thresholdOk ? colorOk : (history[i] < thresholdSlow ? colorSlow : colorFail));
      ctx.fill();
    }
  }

  onMount(() => {
    draw();
  });

  $effect(() => {
    draw();
  });
</script>

<canvas bind:this={canvas} {width} {height} style="width: 100%; height: {height}px; background: transparent; cursor: {onclick ? 'pointer' : 'default'};" onclick={onclick}></canvas>