/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: [
    "./index.html",
    "./src/**/*.{vue,js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      fontFamily: {
        mono: ['JetBrains Mono', 'Menlo', 'Monaco', 'Consolas', 'monospace'],
      },
      colors: {
        accent: '#32CD32',
        n: {
          950: '#111111',
          900: '#1a1a1a',
          800: '#262626',
          600: '#444444',
          500: '#555555',
          400: '#888888',
          300: '#aaaaaa',
        },
      },
    },
  },
  plugins: [],
}
