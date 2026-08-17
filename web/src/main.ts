import { createPinia } from 'pinia'
import { createApp } from 'vue'
import 'element-plus/theme-chalk/base.css'
import 'element-plus/theme-chalk/el-button.css'
import 'element-plus/theme-chalk/el-icon.css'
import 'element-plus/theme-chalk/el-tag.css'
import './styles.css'
import App from './App.vue'

createApp(App).use(createPinia()).mount('#app')
